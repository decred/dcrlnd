package dcrdnotify

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrjson/v3"
	"github.com/decred/dcrd/dcrutil/v3"
	jsontypes "github.com/decred/dcrd/rpc/jsonrpc/types/v2"
	"github.com/decred/dcrd/rpcclient/v6"
	"github.com/decred/dcrd/txscript/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/queue"
)

const (
	// notifierType uniquely identifies this concrete implementation of the
	// ChainNotifier interface.
	notifierType = "dcrd"
)

var (
	// ErrChainNotifierShuttingDown is used when we are trying to
	// measure a spend notification when notifier is already stopped.
	ErrChainNotifierShuttingDown = errors.New("chainntnfs: system interrupt " +
		"while attempting to register for spend notification")

	// errInefficientRescanTxNotFound is used when manually calling the
	// inefficient rescan method.
	errInefficientRescanTxNotFound = errors.New("chainntnfs: tx not found " +
		"after inneficient rescan")
)

type chainConnAdaptor struct {
	c   *rpcclient.Client
	ctx context.Context
}

func (cca *chainConnAdaptor) GetBlockHeader(blockHash *chainhash.Hash) (*wire.BlockHeader, error) {
	return cca.c.GetBlockHeader(cca.ctx, blockHash)
}

func (cca *chainConnAdaptor) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	return cca.c.GetBlockHash(cca.ctx, blockHeight)
}

func (cca *chainConnAdaptor) GetBlockVerbose(hash *chainhash.Hash, b bool) (*jsontypes.GetBlockVerboseResult, error) {
	return cca.c.GetBlockVerbose(cca.ctx, hash, b)
}

func (cca *chainConnAdaptor) GetRawTransactionVerbose(hash *chainhash.Hash) (*jsontypes.TxRawResult, error) {
	return cca.c.GetRawTransactionVerbose(cca.ctx, hash)
}

// TODO(roasbeef): generalize struct below: * move chans to config, allow
// outside callers to handle send conditions

// DcrdNotifier implements the ChainNotifier interface using dcrd's websockets
// notifications. Multiple concurrent clients are supported. All notifications
// are achieved via non-blocking sends on client channels.
type DcrdNotifier struct {
	epochClientCounter uint64 // To be used atomically.

	start   sync.Once
	active  int32 // To be used atomically.
	stopped int32 // To be used atomically.

	chainConn   *rpcclient.Client
	cca         *chainConnAdaptor
	chainParams *chaincfg.Params

	notificationCancels  chan interface{}
	notificationRegistry chan interface{}

	txNotifier *chainntnfs.TxNotifier

	blockEpochClients map[uint64]*blockEpochRegistration

	bestBlock chainntnfs.BlockEpoch

	chainUpdates *queue.ConcurrentQueue

	// spendHintCache is a cache used to query and update the latest height
	// hints for an outpoint. Each height hint represents the earliest
	// height at which the outpoint could have been spent within the chain.
	spendHintCache chainntnfs.SpendHintCache

	// confirmHintCache is a cache used to query the latest height hints for
	// a transaction. Each height hint represents the earliest height at
	// which the transaction could have confirmed within the chain.
	confirmHintCache chainntnfs.ConfirmHintCache

	wg   sync.WaitGroup
	quit chan struct{}
}

// Ensure DcrdNotifier implements the ChainNotifier interface at compile time.
var _ chainntnfs.ChainNotifier = (*DcrdNotifier)(nil)

// New returns a new DcrdNotifier instance. This function assumes the dcrd node
// detailed in the passed configuration is already running, and willing to
// accept new websockets clients.
func New(config *rpcclient.ConnConfig, chainParams *chaincfg.Params,
	spendHintCache chainntnfs.SpendHintCache,
	confirmHintCache chainntnfs.ConfirmHintCache) (*DcrdNotifier, error) {

	notifier := &DcrdNotifier{
		chainParams: chainParams,

		notificationCancels:  make(chan interface{}),
		notificationRegistry: make(chan interface{}),

		blockEpochClients: make(map[uint64]*blockEpochRegistration),

		chainUpdates: queue.NewConcurrentQueue(10),

		spendHintCache:   spendHintCache,
		confirmHintCache: confirmHintCache,

		quit: make(chan struct{}),
	}

	ntfnCallbacks := &rpcclient.NotificationHandlers{
		OnBlockConnected:    notifier.onBlockConnected,
		OnBlockDisconnected: notifier.onBlockDisconnected,
	}

	// Disable connecting to dcrd within the rpcclient.New method. We defer
	// establishing the connection to our .Start() method.
	config.DisableConnectOnNew = true
	config.DisableAutoReconnect = false
	chainConn, err := rpcclient.New(config, ntfnCallbacks)
	if err != nil {
		return nil, err
	}
	notifier.chainConn = chainConn
	notifier.cca = &chainConnAdaptor{c: chainConn, ctx: context.TODO()}

	return notifier, nil
}

// Start connects to the running dcrd node over websockets, registers for block
// notifications, and finally launches all related helper goroutines.
func (n *DcrdNotifier) Start() error {
	var startErr error
	n.start.Do(func() {
		startErr = n.startNotifier()
	})
	return startErr
}

func (n *DcrdNotifier) startNotifier() error {
	chainntnfs.Log.Infof("Starting dcrd notifier")

	n.chainUpdates.Start()

	// TODO(decred): Handle 20 retries...
	//
	// Connect to dcrd, and register for notifications on connected, and
	// disconnected blocks.
	if err := n.chainConn.Connect(context.Background(), true); err != nil {
		n.chainUpdates.Stop()
		return err
	}
	if err := n.chainConn.NotifyBlocks(context.TODO()); err != nil {
		n.chainUpdates.Stop()
		return err
	}

	currentHash, currentHeight, err := n.chainConn.GetBestBlock(context.TODO())
	if err != nil {
		n.chainUpdates.Stop()
		return err
	}

	n.txNotifier = chainntnfs.NewTxNotifier(
		uint32(currentHeight), chainntnfs.ReorgSafetyLimit,
		n.confirmHintCache, n.spendHintCache, n.chainParams,
	)

	n.bestBlock = chainntnfs.BlockEpoch{
		Height: int32(currentHeight),
		Hash:   currentHash,
	}

	n.wg.Add(1)
	go n.notificationDispatcher()

	// Set the active flag now that we've completed the full
	// startup.
	atomic.StoreInt32(&n.active, 1)

	return nil
}

// Started returns true if this instance has been started, and false otherwise.
func (n *DcrdNotifier) Started() bool {
	return atomic.LoadInt32(&n.active) != 0
}

// Stop shutsdown the DcrdNotifier.
func (n *DcrdNotifier) Stop() error {
	// Already shutting down?
	if atomic.AddInt32(&n.stopped, 1) != 1 {
		return nil
	}

	// Shutdown the rpc client, this gracefully disconnects from dcrd, and
	// cleans up all related resources.
	n.chainConn.Shutdown()

	close(n.quit)
	n.wg.Wait()

	n.chainUpdates.Stop()

	// Notify all pending clients of our shutdown by closing the related
	// notification channels.
	for _, epochClient := range n.blockEpochClients {
		close(epochClient.cancelChan)
		epochClient.wg.Wait()

		close(epochClient.epochChan)
	}
	n.txNotifier.TearDown()

	return nil
}

// filteredBlock represents a new block which has been connected to the main
// chain. The slice of transactions will only be populated if the block
// includes a transaction that confirmed one of our watched txids, or spends
// one of the outputs currently being watched.
type filteredBlock struct {
	header *wire.BlockHeader
	txns   []*dcrutil.Tx

	// connected is true if this update is a new block and false if it is a
	// disconnected block.
	connect bool
}

// onBlockConnected implements on OnBlockConnected callback for rpcclient.
func (n *DcrdNotifier) onBlockConnected(blockHeader []byte, transactions [][]byte) {
	var header wire.BlockHeader
	if err := header.FromBytes(blockHeader); err != nil {
		chainntnfs.Log.Warnf("Received block connected with malformed "+
			"header: %v", err)
		return
	}

	txns := make([]*dcrutil.Tx, 0, len(transactions))
	for _, txBytes := range transactions {
		var tx wire.MsgTx
		if err := tx.FromBytes(txBytes); err != nil {
			chainntnfs.Log.Warnf("Received block connected with malformed "+
				"transaction: %v", err)
			return
		}

		txns = append(txns, dcrutil.NewTx(&tx))
	}

	// Append this new chain update to the end of the queue of new chain
	// updates.
	select {
	case n.chainUpdates.ChanIn() <- &filteredBlock{
		header:  &header,
		txns:    txns,
		connect: true,
	}:
	case <-n.quit:
		return
	}
}

// onBlockDisconnected implements on OnBlockDisconnected callback for rpcclient.
func (n *DcrdNotifier) onBlockDisconnected(blockHeader []byte) {
	var header wire.BlockHeader
	if err := header.FromBytes(blockHeader); err != nil {
		chainntnfs.Log.Warnf("Received block disconnected with malformed "+
			"header: %v", err)
		return
	}

	// Append this new chain update to the end of the queue of new chain
	// updates.
	select {
	case n.chainUpdates.ChanIn() <- &filteredBlock{
		header:  &header,
		connect: false,
	}:
	case <-n.quit:
		return
	}
}

// notificationDispatcher is the primary goroutine which handles client
// notification registrations, as well as notification dispatches.
func (n *DcrdNotifier) notificationDispatcher() {
out:
	for {
		select {
		case cancelMsg := <-n.notificationCancels:
			switch msg := cancelMsg.(type) {
			case *epochCancel:
				chainntnfs.Log.Infof("Cancelling epoch "+
					"notification, epoch_id=%v", msg.epochID)

				// First, we'll lookup the original
				// registration in order to stop the active
				// queue goroutine.
				reg := n.blockEpochClients[msg.epochID]
				reg.epochQueue.Stop()

				// Next, close the cancel channel for this
				// specific client, and wait for the client to
				// exit.
				close(n.blockEpochClients[msg.epochID].cancelChan)
				n.blockEpochClients[msg.epochID].wg.Wait()

				// Once the client has exited, we can then
				// safely close the channel used to send epoch
				// notifications, in order to notify any
				// listeners that the intent has been
				// canceled.
				close(n.blockEpochClients[msg.epochID].epochChan)
				delete(n.blockEpochClients, msg.epochID)
			}

		case registerMsg := <-n.notificationRegistry:
			switch msg := registerMsg.(type) {
			case *chainntnfs.HistoricalConfDispatch:
				// Look up whether the transaction/output script
				// has already confirmed in the active chain.
				// We'll do this in a goroutine to prevent
				// blocking potentially long rescans.
				//
				// TODO(wilmer): add retry logic if rescan fails?
				n.wg.Add(1)
				go func() {
					defer n.wg.Done()

					confDetails, _, err := n.historicalConfDetails(
						msg.ConfRequest,
						msg.StartHeight, msg.EndHeight,
					)
					if err != nil {
						chainntnfs.Log.Error(err)
						return
					}

					// If the historical dispatch finished
					// without error, we will invoke
					// UpdateConfDetails even if none were
					// found. This allows the notifier to
					// begin safely updating the height hint
					// cache at tip, since any pending
					// rescans have now completed.
					err = n.txNotifier.UpdateConfDetails(
						msg.ConfRequest, confDetails,
					)
					if err != nil {
						chainntnfs.Log.Error(err)
					}
				}()

			case *blockEpochRegistration:
				chainntnfs.Log.Infof("New block epoch subscription")

				n.blockEpochClients[msg.epochID] = msg

				// If the client did not provide their best
				// known block, then we'll immediately dispatch
				// a notification for the current tip.
				if msg.bestBlock == nil {
					n.notifyBlockEpochClient(
						msg, n.bestBlock.Height,
						n.bestBlock.Hash,
					)

					msg.errorChan <- nil
					continue
				}

				// Otherwise, we'll attempt to deliver the
				// backlog of notifications from their best
				// known block.
				missedBlocks, err := chainntnfs.GetClientMissedBlocks(
					n.cca, msg.bestBlock,
					n.bestBlock.Height, true,
				)
				if err != nil {
					msg.errorChan <- err
					continue
				}

				for _, block := range missedBlocks {
					n.notifyBlockEpochClient(
						msg, block.Height, block.Hash,
					)
				}

				msg.errorChan <- nil
			}

		case item := <-n.chainUpdates.ChanOut():
			update := item.(*filteredBlock)
			header := update.header
			if update.connect {
				if header.PrevBlock != *n.bestBlock.Hash {
					// Handle the case where the notifier
					// missed some blocks from its chain
					// backend
					chainntnfs.Log.Infof("Missed blocks, " +
						"attempting to catch up")
					newBestBlock, missedBlocks, err :=
						chainntnfs.HandleMissedBlocks(
							n.cca,
							n.txNotifier,
							n.bestBlock,
							int32(header.Height),
							true,
						)
					if err != nil {
						// Set the bestBlock here in case
						// a catch up partially completed.
						n.bestBlock = newBestBlock
						chainntnfs.Log.Error(err)
						continue
					}

					for _, block := range missedBlocks {
						filteredBlock, err := n.fetchFilteredBlock(block)
						if err != nil {
							chainntnfs.Log.Error(err)
							continue out
						}

						err = n.handleBlockConnected(filteredBlock)
						if err != nil {
							chainntnfs.Log.Error(err)
							continue out
						}
					}
				}

				// TODO(decred) Discuss and decide how to do this.
				// This is necessary because in dcrd, OnBlockConnected will
				// only return filtered transactions, so we need to actually
				// load a watched transaction using LoadTxFilter (which is
				// currently not done in RegisterConfirmationsNtfn).
				bh := update.header.BlockHash()
				filteredBlock, err := n.fetchFilteredBlockForBlockHash(&bh)
				if err != nil {
					chainntnfs.Log.Error(err)
					continue
				}

				if err := n.handleBlockConnected(filteredBlock); err != nil {
					chainntnfs.Log.Error(err)
				}
				continue
			}

			if header.Height != uint32(n.bestBlock.Height) {
				chainntnfs.Log.Infof("Missed disconnected" +
					"blocks, attempting to catch up")
			}

			newBestBlock, err := chainntnfs.RewindChain(
				n.cca, n.txNotifier, n.bestBlock,
				int32(header.Height-1),
			)
			if err != nil {
				chainntnfs.Log.Errorf("Unable to rewind chain "+
					"from height %d to height %d: %v",
					n.bestBlock.Height, int32(header.Height-1), err)
			}

			// Set the bestBlock here in case a chain rewind
			// partially completed.
			n.bestBlock = newBestBlock

		case <-n.quit:
			break out
		}
	}
	n.wg.Done()
}

// historicalConfDetails looks up whether a confirmation request (txid/output
// script) has already been included in a block in the active chain and, if so,
// returns details about said block.
func (n *DcrdNotifier) historicalConfDetails(confRequest chainntnfs.ConfRequest,
	startHeight, endHeight uint32) (*chainntnfs.TxConfirmation,
	chainntnfs.TxConfStatus, error) {

	// If a txid was not provided, then we should dispatch upon seeing the
	// script on-chain, so we'll short-circuit straight to scanning manually
	// as there doesn't exist a script index to query.
	if confRequest.TxID == chainntnfs.ZeroHash {
		return n.confDetailsManually(
			confRequest, startHeight, endHeight,
		)
	}

	// Otherwise, we'll dispatch upon seeing a transaction on-chain with the
	// given hash.
	//
	// We'll first attempt to retrieve the transaction using the node's
	// txindex.
	txNotFoundErr := "No information available about transaction"
	txConf, txStatus, err := chainntnfs.ConfDetailsFromTxIndex(
		n.cca, confRequest, txNotFoundErr,
	)

	// We'll then check the status of the transaction lookup returned to
	// determine whether we should proceed with any fallback methods.
	switch {

	// We failed querying the index for the transaction, fall back to
	// scanning manually.
	case err != nil:
		chainntnfs.Log.Debugf("Unable to determine confirmation of %v "+
			"through the backend's txindex (%v), scanning manually",
			confRequest.TxID, err)

		return n.confDetailsManually(
			confRequest, startHeight, endHeight,
		)

	// The transaction was found within the node's mempool.
	case txStatus == chainntnfs.TxFoundMempool:

	// The transaction was found within the node's txindex.
	case txStatus == chainntnfs.TxFoundIndex:

	// The transaction was not found within the node's mempool or txindex.
	case txStatus == chainntnfs.TxNotFoundIndex:

	// Unexpected txStatus returned.
	default:
		return nil, txStatus,
			fmt.Errorf("got unexpected txConfStatus: %v", txStatus)
	}

	return txConf, txStatus, nil
}

// confDetailsManually looks up whether a transaction/output script has already
// been included in a block in the active chain by scanning the chain's blocks
// within the given range. If the transaction/output script is found, its
// confirmation details are returned. Otherwise, nil is returned.
func (n *DcrdNotifier) confDetailsManually(confRequest chainntnfs.ConfRequest,
	startHeight, endHeight uint32) (*chainntnfs.TxConfirmation,
	chainntnfs.TxConfStatus, error) {

	// Begin scanning blocks at every height to determine where the
	// transaction was included in.
	for height := endHeight; height >= startHeight && height > 0; height-- {
		// Ensure we haven't been requested to shut down before
		// processing the next height.
		select {
		case <-n.quit:
			return nil, chainntnfs.TxNotFoundManually,
				chainntnfs.ErrChainNotifierShuttingDown
		default:
		}

		blockHash, err := n.chainConn.GetBlockHash(context.TODO(), int64(height))
		if err != nil {
			return nil, chainntnfs.TxNotFoundManually,
				fmt.Errorf("unable to get hash from block "+
					"with height %d", height)
		}

		// TODO: fetch the neutrino filters instead.
		block, err := n.chainConn.GetBlock(context.TODO(), blockHash)
		if err != nil {
			return nil, chainntnfs.TxNotFoundManually,
				fmt.Errorf("unable to get block with hash "+
					"%v: %v", blockHash, err)
		}

		// For every transaction in the block, check which one matches
		// our request. If we find one that does, we can dispatch its
		// confirmation details.
		for txIndex, tx := range block.Transactions {
			if !confRequest.MatchesTx(tx) {
				continue
			}

			return &chainntnfs.TxConfirmation{
				Tx:          tx,
				BlockHash:   blockHash,
				BlockHeight: height,
				TxIndex:     uint32(txIndex),
			}, chainntnfs.TxFoundManually, nil
		}
	}

	// If we reach here, then we were not able to find the transaction
	// within a block, so we avoid returning an error.
	return nil, chainntnfs.TxNotFoundManually, nil
}

// handleBlockConnected applies a chain update for a new block. Any watched
// transactions included this block will processed to either send notifications
// now or after numConfirmations confs.
func (n *DcrdNotifier) handleBlockConnected(newBlock *filteredBlock) error {
	// We'll then extend the txNotifier's height with the information of
	// this new block, which will handle all of the notification logic for
	// us.
	newBlockHash := newBlock.header.BlockHash()
	newBlockHeight := newBlock.header.Height
	err := n.txNotifier.ConnectTip(
		&newBlockHash, newBlockHeight, newBlock.txns,
	)
	if err != nil {
		return fmt.Errorf("unable to connect tip: %v", err)
	}

	chainntnfs.Log.Infof("New block: height=%v, hash=%v", newBlockHeight,
		newBlockHash)

	// Now that we've guaranteed the new block extends the txNotifier's
	// current tip, we'll proceed to dispatch notifications to all of our
	// registered clients whom have had notifications fulfilled. Before
	// doing so, we'll make sure update our in memory state in order to
	// satisfy any client requests based upon the new block.
	n.bestBlock.Hash = &newBlockHash
	n.bestBlock.Height = int32(newBlockHeight)

	n.notifyBlockEpochs(int32(newBlockHeight), &newBlockHash)
	return n.txNotifier.NotifyHeight(newBlockHeight)
}

// fetchFilteredBlock is a utility to retrieve the full filtered block from a
// block epoch.
func (n *DcrdNotifier) fetchFilteredBlock(epoch chainntnfs.BlockEpoch) (*filteredBlock, error) {
	return n.fetchFilteredBlockForBlockHash(epoch.Hash)
}

// fetchFilteredBlockForBlockHash is a utility to retrieve the full filtered
// block (including _all_ transactions, not just the watched ones) for the
// block identified by the provided block hash.
func (n *DcrdNotifier) fetchFilteredBlockForBlockHash(bh *chainhash.Hash) (*filteredBlock, error) {
	rawBlock, err := n.chainConn.GetBlock(context.TODO(), bh)
	if err != nil {
		return nil, fmt.Errorf("unable to get block: %v", err)
	}

	txns := make([]*dcrutil.Tx, 0, len(rawBlock.Transactions))
	for i := range rawBlock.Transactions {
		tx := dcrutil.NewTx(rawBlock.Transactions[i])
		tx.SetIndex(i)
		tx.SetTree(wire.TxTreeRegular)
		txns = append(txns, tx)
	}

	block := &filteredBlock{
		header:  &rawBlock.Header,
		txns:    txns,
		connect: true,
	}
	return block, nil
}

// notifyBlockEpochs notifies all registered block epoch clients of the newly
// connected block to the main chain.
func (n *DcrdNotifier) notifyBlockEpochs(newHeight int32, newHash *chainhash.Hash) {
	for _, client := range n.blockEpochClients {
		n.notifyBlockEpochClient(client, newHeight, newHash)
	}
}

// notifyBlockEpochClient sends a registered block epoch client a notification
// about a specific block.
func (n *DcrdNotifier) notifyBlockEpochClient(epochClient *blockEpochRegistration,
	height int32, hash *chainhash.Hash) {

	epoch := &chainntnfs.BlockEpoch{
		Height: height,
		Hash:   hash,
	}

	select {
	case epochClient.epochQueue.ChanIn() <- epoch:
	case <-epochClient.cancelChan:
	case <-n.quit:
	}
}

// RegisterSpendNtfn registers an intent to be notified once the target
// outpoint/output script has been spent by a transaction on-chain. When
// intending to be notified of the spend of an output script, a nil outpoint
// must be used. The heightHint should represent the earliest height in the
// chain of the transaction that spent the outpoint/output script.
//
// Once a spend of has been detected, the details of the spending event will be
// sent across the 'Spend' channel.
func (n *DcrdNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {

	// Register the conf notification with the TxNotifier. A non-nil value
	// for `dispatch` will be returned if we are required to perform a
	// manual scan for the confirmation. Otherwise the notifier will begin
	// watching at tip for the transaction to confirm.
	ntfn, err := n.txNotifier.RegisterSpend(outpoint, pkScript, heightHint)
	if err != nil {
		return nil, err
	}

	// If the txNotifier didn't return any details to perform a historical
	// scan of the chain, then we can return early as there's nothing left
	// for us to do.
	if ntfn.HistoricalDispatch == nil {
		return ntfn.Event, nil
	}

	// TODO(decred) This currently always only adds to the tx filter, which
	// will make it grow unboundedly. Ideally this should be reloaded with
	// the specific set we're interested in, but that would require
	// rebuilding the tx filter every time this is called.
	//
	// We'll then request the backend to notify us when it has detected the
	// outpoint or a script was spent.
	var ops []wire.OutPoint
	var addrs []dcrutil.Address

	// Otherwise, we'll determine when the output was spent by scanning the
	// chain.  We'll begin by determining where to start our historical
	// rescan.
	startHeight := ntfn.HistoricalDispatch.StartHeight

	emptyOutPoint := outpoint == nil || *outpoint == chainntnfs.ZeroOutPoint
	if emptyOutPoint {
		_, addrs, _, err = txscript.ExtractPkScriptAddrs(
			0, pkScript, n.chainParams, false,
		)
		if err != nil {
			return nil, fmt.Errorf("unable to parse address: %v", err)
		}
	} else {
		ops = []wire.OutPoint{*outpoint}
	}

	// Ensure we'll receive any new notifications for either the outpoint
	// or the address from now on.
	if err := n.chainConn.LoadTxFilter(context.TODO(), false, addrs, ops); err != nil {
		return nil, err
	}

	if !emptyOutPoint {
		// When dispatching spends of outpoints, there are a number of checks
		// we can make to start our rescan from a better height or completely
		// avoid it.
		//
		// We'll start by checking the backend's UTXO set to determine whether
		// the outpoint has been spent. If it hasn't, we can return to the
		// caller as well.
		txOut, err := n.chainConn.GetTxOut(
			context.TODO(), &outpoint.Hash, outpoint.Index, true,
		)
		if err != nil {
			return nil, err
		}
		if txOut != nil {
			// We'll let the txNotifier know the outpoint is still
			// unspent in order to begin updating its spend hint.
			err := n.txNotifier.UpdateSpendDetails(
				ntfn.HistoricalDispatch.SpendRequest, nil,
			)
			if err != nil {
				return nil, err
			}

			return ntfn.Event, nil
		}

		// As a minimal optimization, we'll query the backend's
		// transaction index (if enabled) to determine if we have a
		// better rescan starting height. We can do this as the
		// GetRawTransaction call will return the hash of the block it
		// was included in within the chain.
		tx, err := n.chainConn.GetRawTransactionVerbose(context.TODO(), &outpoint.Hash)
		if err != nil {
			// Avoid returning an error if the transaction was not found to
			// proceed with fallback methods.
			isNoTxIndexErr := chainntnfs.IsTxIndexDisabledError(err)
			jsonErr, ok := err.(*dcrjson.RPCError)
			if !isNoTxIndexErr && (!ok || jsonErr.Code != dcrjson.ErrRPCNoTxInfo) {
				return nil, fmt.Errorf("unable to query for "+
					"txid %v: %v", outpoint.Hash, err)
			}
		}

		// If the transaction index was enabled, we'll use the block's
		// hash to retrieve its height and check whether it provides a
		// better starting point for our rescan.
		if tx != nil {
			// If the transaction containing the outpoint hasn't confirmed
			// on-chain, then there's no need to perform a rescan.
			if tx.BlockHash == "" {
				return ntfn.Event, nil
			}

			blockHash, err := chainhash.NewHashFromStr(tx.BlockHash)
			if err != nil {
				return nil, err
			}
			blockHeader, err := n.chainConn.GetBlockHeader(context.TODO(), blockHash)
			if err != nil {
				return nil, fmt.Errorf("unable to get header for "+
					"block %v: %v", blockHash, err)
			}

			if blockHeader.Height > ntfn.HistoricalDispatch.StartHeight {
				startHeight = blockHeader.Height
			}
		}
	}

	// TODO(decred): Fix!
	//
	// In order to ensure that we don't block the caller on what may be a
	// long rescan, we'll launch a new goroutine to handle the async result
	// of the rescan. We purposefully prevent from adding this goroutine to
	// the WaitGroup as we cannot wait for a quit signal due to the
	// asyncResult channel not being exposed.
	//
	// TODO(wilmer): add retry logic if rescan fails?
	go n.inefficientSpendRescan(startHeight, ntfn.HistoricalDispatch)

	return ntfn.Event, nil
}

// txSpendsSpendRequest returns the index where the given spendRequest was
// spent by the transaction or -1 if no inputs spend the given spendRequest.
func txSpendsSpendRequest(tx *wire.MsgTx, spendRequest *chainntnfs.SpendRequest,
	addrParams dcrutil.AddressParams) int {

	if spendRequest.OutPoint != chainntnfs.ZeroOutPoint {
		// Matching by outpoint.
		for i, in := range tx.TxIn {
			if in.PreviousOutPoint == spendRequest.OutPoint {
				return i
			}
		}
		return -1
	}

	// Matching by script.
	for i, in := range tx.TxIn {
		// Ignore the errors here, due to them definitely not being a
		// match.
		pkScript, _ := chainscan.ComputePkScript(
			spendRequest.PkScript.ScriptVersion(), in.SignatureScript,
		)
		if spendRequest.PkScript.Equal(&pkScript) {
			return i
		}
	}
	return -1
}

// inefficientSpendRescan is a utility function to RegisterSpendNtfn. It performs
// a (very) inefficient rescan over the full mined block database, looking
// for the spending of the passed ntfn outpoint.
//
// This needs to be executed in its own goroutine, as it blocks.
//
// TODO(decred) This _needs_ to be improved into a proper rescan procedure or
// an index.
func (n *DcrdNotifier) inefficientSpendRescan(startHeight uint32,
	histDispatch *chainntnfs.HistoricalSpendDispatch) (*chainntnfs.SpendDetail, error) {

	endHeight := int64(histDispatch.EndHeight)

	for height := int64(startHeight); height <= endHeight; height++ {
		scanHash, err := n.chainConn.GetBlockHash(context.TODO(), height)
		if err != nil {
			chainntnfs.Log.Errorf("Error determining next block to scan for "+
				"outpoint spender", err)
			return nil, err
		}

		res, err := n.chainConn.Rescan(context.TODO(), []chainhash.Hash{*scanHash})
		if err != nil {
			chainntnfs.Log.Errorf("Rescan to determine the spend "+
				"details of %v failed: %v", histDispatch.SpendRequest.OutPoint, err)
			return nil, err
		}

		if len(res.DiscoveredData) == 0 {
			// No data found for this block, so go on to the next.
			continue
		}

		// We need to check individual txs since the active tx filter
		// might have multiple transactions, and they may be repeatedly
		// encountered.
		for _, data := range res.DiscoveredData {
			for _, hexTx := range data.Transactions {
				bytesTx, err := hex.DecodeString(hexTx)
				if err != nil {
					chainntnfs.Log.Errorf("Error converting hexTx to "+
						"bytes during spend rescan: %v", err)
					return nil, err
				}

				var tx wire.MsgTx
				err = tx.FromBytes(bytesTx)
				if err != nil {
					chainntnfs.Log.Errorf("Error decoding tx from bytes "+
						"during spend rescan: %v", err)
				}

				spenderIndex := txSpendsSpendRequest(
					&tx, &histDispatch.SpendRequest,
					n.chainParams,
				)
				if spenderIndex == -1 {
					// This tx is not a match, so go on to
					// the next.
					continue
				}

				// Found the spender tx! Update the spend
				// status (which will emit the notification)
				// and finish the scan.
				txHash := tx.TxHash()
				details := &chainntnfs.SpendDetail{
					SpentOutPoint:     &histDispatch.SpendRequest.OutPoint,
					SpenderTxHash:     &txHash,
					SpendingTx:        &tx,
					SpenderInputIndex: uint32(spenderIndex),
					SpendingHeight:    int32(height),
				}
				err = n.txNotifier.UpdateSpendDetails(histDispatch.SpendRequest, details)
				return details, err
			}
		}
	}

	return nil, errInefficientRescanTxNotFound
}

// RegisterConfirmationsNtfn registers an intent to be notified once the target
// txid/output script has reached numConfs confirmations on-chain. When
// intending to be notified of the confirmation of an output script, a nil txid
// must be used. The heightHint should represent the earliest height at which
// the txid/output script could have been included in the chain.
//
// Progress on the number of confirmations left can be read from the 'Updates'
// channel. Once it has reached all of its confirmations, a notification will be
// sent across the 'Confirmed' channel.
func (n *DcrdNotifier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
	pkScript []byte,
	numConfs, heightHint uint32) (*chainntnfs.ConfirmationEvent, error) {

	// Register the conf notification with the TxNotifier. A non-nil value
	// for `dispatch` will be returned if we are required to perform a
	// manual scan for the confirmation. Otherwise the notifier will begin
	// watching at tip for the transaction to confirm.
	ntfn, err := n.txNotifier.RegisterConf(
		txid, pkScript, numConfs, heightHint,
	)
	if err != nil {
		return nil, err
	}

	if ntfn.HistoricalDispatch == nil {
		return ntfn.Event, nil
	}

	select {
	case n.notificationRegistry <- ntfn.HistoricalDispatch:
		return ntfn.Event, nil
	case <-n.quit:
		return nil, chainntnfs.ErrChainNotifierShuttingDown
	}
}

// blockEpochRegistration represents a client's intent to receive a
// notification with each newly connected block.
type blockEpochRegistration struct {
	epochID uint64

	epochChan chan *chainntnfs.BlockEpoch

	epochQueue *queue.ConcurrentQueue

	bestBlock *chainntnfs.BlockEpoch

	errorChan chan error

	cancelChan chan struct{}

	wg sync.WaitGroup
}

// epochCancel is a message sent to the DcrdNotifier when a client wishes to
// cancel an outstanding epoch notification that has yet to be dispatched.
type epochCancel struct {
	epochID uint64
}

// RegisterBlockEpochNtfn returns a BlockEpochEvent which subscribes the
// caller to receive notifications, of each new block connected to the main
// chain. Clients have the option of passing in their best known block, which
// the notifier uses to check if they are behind on blocks and catch them up.
// If they do not provide one, then a notification will be dispatched
// immediately for the current tip of the chain upon a successful registration.
func (n *DcrdNotifier) RegisterBlockEpochNtfn(
	bestBlock *chainntnfs.BlockEpoch) (*chainntnfs.BlockEpochEvent, error) {

	reg := &blockEpochRegistration{
		epochQueue: queue.NewConcurrentQueue(20),
		epochChan:  make(chan *chainntnfs.BlockEpoch, 20),
		cancelChan: make(chan struct{}),
		epochID:    atomic.AddUint64(&n.epochClientCounter, 1),
		bestBlock:  bestBlock,
		errorChan:  make(chan error, 1),
	}

	reg.epochQueue.Start()

	// Before we send the request to the main goroutine, we'll launch a new
	// goroutine to proxy items added to our queue to the client itself.
	// This ensures that all notifications are received *in order*.
	reg.wg.Add(1)
	go func() {
		defer reg.wg.Done()

		for {
			select {
			case ntfn := <-reg.epochQueue.ChanOut():
				blockNtfn := ntfn.(*chainntnfs.BlockEpoch)
				select {
				case reg.epochChan <- blockNtfn:

				case <-reg.cancelChan:
					return

				case <-n.quit:
					return
				}

			case <-reg.cancelChan:
				return

			case <-n.quit:
				return
			}
		}
	}()

	select {
	case <-n.quit:
		// As we're exiting before the registration could be sent,
		// we'll stop the queue now ourselves.
		reg.epochQueue.Stop()

		return nil, errors.New("chainntnfs: system interrupt while " +
			"attempting to register for block epoch notification")
	case n.notificationRegistry <- reg:
		return &chainntnfs.BlockEpochEvent{
			Epochs: reg.epochChan,
			Cancel: func() {
				cancel := &epochCancel{
					epochID: reg.epochID,
				}

				// Submit epoch cancellation to notification dispatcher.
				select {
				case n.notificationCancels <- cancel:
					// Cancellation is being handled, drain
					// the epoch channel until it is closed
					// before yielding to caller.
					for {
						select {
						case _, ok := <-reg.epochChan:
							if !ok {
								return
							}
						case <-n.quit:
							return
						}
					}
				case <-n.quit:
				}
			},
		}, nil
	}
}
