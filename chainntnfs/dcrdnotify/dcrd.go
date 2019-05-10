package dcrdnotify

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrjson/v2"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/rpcclient/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
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
)

// TODO(roasbeef): generalize struct below:
//  * move chans to config, allow outside callers to handle send conditions

// DcrdNotifier implements the ChainNotifier interface using dcrd's websockets
// notifications. Multiple concurrent clients are supported. All notifications
// are achieved via non-blocking sends on client channels.
type DcrdNotifier struct {
	confClientCounter  uint64 // To be used atomically.
	spendClientCounter uint64 // To be used atomically.
	epochClientCounter uint64 // To be used atomically.

	started int32 // To be used atomically.
	stopped int32 // To be used atomically.

	chainConn *rpcclient.Client

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
func New(config *rpcclient.ConnConfig, spendHintCache chainntnfs.SpendHintCache,
	confirmHintCache chainntnfs.ConfirmHintCache) (*DcrdNotifier, error) {

	notifier := &DcrdNotifier{
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

	// Disable connecting to dcrd within the rpcclient.New method. We
	// defer establishing the connection to our .Start() method.
	config.DisableConnectOnNew = true
	config.DisableAutoReconnect = false
	chainConn, err := rpcclient.New(config, ntfnCallbacks)
	if err != nil {
		return nil, err
	}
	notifier.chainConn = chainConn

	return notifier, nil
}

// Start connects to the running dcrd node over websockets, registers for block
// notifications, and finally launches all related helper goroutines.
func (n *DcrdNotifier) Start() error {
	// Already started?
	if atomic.AddInt32(&n.started, 1) != 1 {
		return nil
	}

	chainntnfs.Log.Infof("Starting dcrd notifier")

	// TODO(decred): Handle 20 retries...
	//
	// Connect to dcrd, and register for notifications on connected, and
	// disconnected blocks.
	if err := n.chainConn.Connect(context.Background(), true); err != nil {
		return err
	}
	if err := n.chainConn.NotifyBlocks(); err != nil {
		return err
	}

	currentHash, currentHeight, err := n.chainConn.GetBestBlock()
	if err != nil {
		return err
	}

	n.txNotifier = chainntnfs.NewTxNotifier(
		uint32(currentHeight), chainntnfs.ReorgSafetyLimit,
		n.confirmHintCache, n.spendHintCache,
	)

	n.bestBlock = chainntnfs.BlockEpoch{
		Height: int32(currentHeight),
		Hash:   currentHash,
	}

	n.chainUpdates.Start()

	n.wg.Add(1)
	go n.notificationDispatcher()

	return nil
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
	n.chainUpdates.ChanIn() <- &filteredBlock{
		header:  &header,
		txns:    txns,
		connect: true,
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
	n.chainUpdates.ChanIn() <- &filteredBlock{
		header:  &header,
		connect: false,
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
				// cancelled.
				close(n.blockEpochClients[msg.epochID].epochChan)
				delete(n.blockEpochClients, msg.epochID)
			}

		case registerMsg := <-n.notificationRegistry:
			switch msg := registerMsg.(type) {
			case *chainntnfs.HistoricalConfDispatch:
				// Look up whether the transaction is already
				// included in the active chain. We'll do this
				// in a goroutine to prevent blocking
				// potentially long rescans.
				//
				// TODO(wilmer): add retry logic if rescan fails?
				n.wg.Add(1)
				go func() {
					defer n.wg.Done()

					confDetails, _, err := n.historicalConfDetails(
						msg.TxID, msg.StartHeight, msg.EndHeight,
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
						*msg.TxID, confDetails,
					)
					if err != nil {
						chainntnfs.Log.Error(err)
					}
				}()

			case *blockEpochRegistration:
				chainntnfs.Log.Infof("New block epoch subscription")
				n.blockEpochClients[msg.epochID] = msg
				if msg.bestBlock != nil {
					missedBlocks, err :=
						chainntnfs.GetClientMissedBlocks(
							n.chainConn, msg.bestBlock,
							n.bestBlock.Height, true,
						)
					if err != nil {
						msg.errorChan <- err
						continue
					}
					for _, block := range missedBlocks {
						n.notifyBlockEpochClient(msg,
							block.Height, block.Hash)
					}

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
							n.chainConn,
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
				n.chainConn, n.txNotifier, n.bestBlock,
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

// historicalConfDetails looks up whether a transaction is already included in a
// block in the active chain and, if so, returns details about the confirmation.
func (n *DcrdNotifier) historicalConfDetails(txid *chainhash.Hash,
	startHeight, endHeight uint32) (*chainntnfs.TxConfirmation,
	chainntnfs.TxConfStatus, error) {

	// We'll first attempt to retrieve the transaction using the node's
	// txindex.
	txConf, txStatus, err := n.confDetailsFromTxIndex(txid)

	// We'll then check the status of the transaction lookup returned to
	// determine whether we should proceed with any fallback methods.
	switch {

	// We failed querying the index for the transaction, fall back to
	// scanning manually.
	case err != nil:
		chainntnfs.Log.Debugf("Failed getting conf details from "+
			"index (%v), scanning manually", err)
		return n.confDetailsManually(txid, startHeight, endHeight)

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

// confDetailsFromTxIndex looks up whether a transaction is already included in
// a block in the active chain by using the backend node's transaction index.
// If the transaction is found its TxConfStatus is returned. If it was found in
// the mempool this will be TxFoundMempool, if it is found in a block this will
// be TxFoundIndex. Otherwise TxNotFoundIndex is returned. If the tx is found
// in a block its confirmation details are also returned.
func (n *DcrdNotifier) confDetailsFromTxIndex(txid *chainhash.Hash) (
	*chainntnfs.TxConfirmation, chainntnfs.TxConfStatus, error) {

	// If the transaction has some or all of its confirmations required,
	// then we may be able to dispatch it immediately.
	tx, err := n.chainConn.GetRawTransactionVerbose(txid)
	if err != nil {
		// If the transaction lookup was successful, but it wasn't found
		// within the index itself, then we can exit early. We'll also
		// need to look at the error message returned as the error code
		// is used for multiple errors.
		txNotFoundErr := "No information available about transaction"
		jsonErr, ok := err.(*dcrjson.RPCError)
		if ok && jsonErr.Code == dcrjson.ErrRPCNoTxInfo &&
			strings.Contains(jsonErr.Message, txNotFoundErr) {

			return nil, chainntnfs.TxNotFoundIndex, nil
		}

		return nil, chainntnfs.TxNotFoundIndex,
			fmt.Errorf("unable to query for txid %v: %v", txid, err)
	}

	// Make sure we actually retrieved a transaction that is included in a
	// block. If not, the transaction must be unconfirmed (in the mempool),
	// and we'll return TxFoundMempool together with a nil TxConfirmation.
	if tx.BlockHash == "" {
		return nil, chainntnfs.TxFoundMempool, nil
	}

	// As we need to fully populate the returned TxConfirmation struct,
	// grab the block in which the transaction was confirmed so we can
	// locate its exact index within the block.
	blockHash, err := chainhash.NewHashFromStr(tx.BlockHash)
	if err != nil {
		return nil, chainntnfs.TxNotFoundIndex,
			fmt.Errorf("unable to get block hash %v for "+
				"historical dispatch: %v", tx.BlockHash, err)
	}

	block, err := n.chainConn.GetBlockVerbose(blockHash, false)
	if err != nil {
		return nil, chainntnfs.TxNotFoundIndex,
			fmt.Errorf("unable to get block with hash %v for "+
				"historical dispatch: %v", blockHash, err)
	}

	// If the block was obtained, locate the transaction's index within the
	// block so we can give the subscriber full confirmation details.
	targetTxidStr := txid.String()
	for txIndex, txHash := range block.Tx {
		if txHash == targetTxidStr {
			details := &chainntnfs.TxConfirmation{
				BlockHash:   blockHash,
				BlockHeight: uint32(block.Height),
				TxIndex:     uint32(txIndex),
			}
			return details, chainntnfs.TxFoundIndex, nil
		}
	}

	// We return an error because we should have found the transaction
	// within the block, but didn't.
	return nil, chainntnfs.TxNotFoundIndex,
		fmt.Errorf("unable to locate tx %v in block %v", txid,
			blockHash)
}

// confDetailsManually looks up whether a transaction is already included in a
// block in the active chain by scanning the chain's blocks, starting from the
// earliest height the transaction could have been included in, to the current
// height in the chain. If the transaction is found, its confirmation details
// are returned. Otherwise, nil is returned.
func (n *DcrdNotifier) confDetailsManually(txid *chainhash.Hash, startHeight,
	endHeight uint32) (*chainntnfs.TxConfirmation,
	chainntnfs.TxConfStatus, error) {

	targetTxidStr := txid.String()

	// Begin scanning blocks at every height to determine where the
	// transaction was included in.
	for height := endHeight; height >= startHeight && height > 0; height-- {
		// Ensure we haven't been requested to shut down before
		// processing the next height.
		select {
		case <-n.quit:
			return nil, chainntnfs.TxNotFoundManually,
				ErrChainNotifierShuttingDown
		default:
		}

		blockHash, err := n.chainConn.GetBlockHash(int64(height))
		if err != nil {
			return nil, chainntnfs.TxNotFoundManually,
				fmt.Errorf("unable to get hash from block "+
					"with height %d", height)
		}

		// TODO: fetch the neutrino filters instead.
		block, err := n.chainConn.GetBlockVerbose(blockHash, false)
		if err != nil {
			return nil, chainntnfs.TxNotFoundManually,
				fmt.Errorf("unable to get block with hash "+
					"%v: %v", blockHash, err)
		}

		for txIndex, txHash := range block.Tx {
			// If we're able to find the transaction in this block,
			// return its confirmation details.
			if txHash == targetTxidStr {
				details := &chainntnfs.TxConfirmation{
					BlockHash:   blockHash,
					BlockHeight: height,
					TxIndex:     uint32(txIndex),
				}
				return details, chainntnfs.TxFoundManually, nil
			}
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
	rawBlock, err := n.chainConn.GetBlock(bh)
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
// outpoint has been spent by a transaction on-chain. Once a spend of the target
// outpoint has been detected, the details of the spending event will be sent
// across the 'Spend' channel. The heightHint should represent the earliest
// height in the chain where the transaction could have been spent in.
func (n *DcrdNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {

	// First, we'll construct a spend notification request and hand it off
	// to the txNotifier.
	spendID := atomic.AddUint64(&n.spendClientCounter, 1)
	cancel := func() {
		n.txNotifier.CancelSpend(*outpoint, spendID)
	}
	ntfn := &chainntnfs.SpendNtfn{
		SpendID:    spendID,
		OutPoint:   *outpoint,
		PkScript:   pkScript,
		Event:      chainntnfs.NewSpendEvent(cancel),
		HeightHint: heightHint,
	}

	historicalDispatch, err := n.txNotifier.RegisterSpend(ntfn)
	if err != nil {
		return nil, err
	}

	// If the txNotifier didn't return any details to perform a historical
	// scan of the chain, then we can return early as there's nothing left
	// for us to do.
	if historicalDispatch == nil {
		return ntfn.Event, nil
	}

	// TODO(decred) This currently always only adds to the tx filter, which will
	// make it grow unboundedly. Ideally this should be reloaded with the
	// specific set we're interested in, but that would require rebuilding the
	// tx filter every time this is called.
	//
	// We'll then request the backend to notify us when it has detected the
	// outpoint as spent.
	ops := []wire.OutPoint{*outpoint}
	if err := n.chainConn.LoadTxFilter(false, nil, ops); err != nil {
		return nil, err
	}

	// In addition to the check above, we'll also check the backend's UTXO
	// set to determine whether the outpoint has been spent. If it hasn't,
	// we can return to the caller as well.
	txOut, err := n.chainConn.GetTxOut(&outpoint.Hash, outpoint.Index, true)
	if err != nil {
		return nil, err
	}
	if txOut != nil {
		// We'll let the txNotifier know the outpoint is still unspent
		// in order to begin updating its spend hint.
		err := n.txNotifier.UpdateSpendDetails(*outpoint, nil)
		if err != nil {
			return nil, err
		}

		return ntfn.Event, nil
	}

	// Otherwise, we'll determine when the output was spent by scanning the
	// chain. We'll begin by determining where to start our historical
	// rescan.
	startHeight := historicalDispatch.StartHeight

	// As a minimal optimization, we'll query the backend's transaction
	// index (if enabled) to determine if we have a better rescan starting
	// height. We can do this as the GetRawTransaction call will return the
	// hash of the block it was included in within the chain.
	tx, err := n.chainConn.GetRawTransactionVerbose(&outpoint.Hash)
	if err != nil {
		// Avoid returning an error if the transaction was not found to
		// proceed with fallback methods.
		jsonErr, ok := err.(*dcrjson.RPCError)
		if !ok || jsonErr.Code != dcrjson.ErrRPCNoTxInfo {
			return nil, fmt.Errorf("unable to query for "+
				"txid %v: %v", outpoint.Hash, err)
		}
	}

	// If the transaction index was enabled, we'll use the block's hash to
	// retrieve its height and check whether it provides a better starting
	// point for our rescan.
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
		blockHeader, err := n.chainConn.GetBlockHeader(blockHash)
		if err != nil {
			return nil, fmt.Errorf("unable to get header for "+
				"block %v: %v", blockHash, err)
		}

		if blockHeader.Height > historicalDispatch.StartHeight {
			startHeight = blockHeader.Height
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
	go n.inefficientSpendRescan(startHeight, ntfn)

	return ntfn.Event, nil
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
	ntfn *chainntnfs.SpendNtfn) {

	_, endHeight, err := n.chainConn.GetBestBlock()
	if err != nil {
		chainntnfs.Log.Errorf("Error determining best block on initial "+
			"rescan: %v", err)
		return
	}

	height := int64(startHeight)

	for height <= endHeight {
		scanHash, err := n.chainConn.GetBlockHash(height)
		if err != nil {
			chainntnfs.Log.Errorf("Error determining next block to scan for "+
				"outpoint spender", err)
			return
		}

		res, err := n.chainConn.Rescan([]chainhash.Hash{*scanHash})
		if err != nil {
			chainntnfs.Log.Errorf("Rescan to determine the spend "+
				"details of %v failed: %v", ntfn.OutPoint, err)
			return
		}

		if len(res.DiscoveredData) > 0 {
			// We need to check individual txs since the active tx filter might
			// have multiple transactions, and they may be repeatedly
			// encountered.
			for _, data := range res.DiscoveredData {
				for _, hexTx := range data.Transactions {
					bytesTx, err := hex.DecodeString(hexTx)
					if err != nil {
						chainntnfs.Log.Errorf("Error converting hexTx to "+
							"bytes during spend rescan: %v", err)
						return
					}

					var tx wire.MsgTx
					err = tx.FromBytes(bytesTx)
					if err != nil {
						chainntnfs.Log.Errorf("Error decoding tx from bytes "+
							"during spend rescan: %v", err)
					}

					for i, in := range tx.TxIn {
						if in.PreviousOutPoint == ntfn.OutPoint {
							// Found the spender tx! Update the spend status
							// (which will emit the notification) and finish the
							// scan.

							txHash := tx.TxHash()
							details := &chainntnfs.SpendDetail{
								SpentOutPoint:     &ntfn.OutPoint,
								SpenderTxHash:     &txHash,
								SpendingTx:        &tx,
								SpenderInputIndex: uint32(i),
								SpendingHeight:    int32(startHeight),
							}
							n.txNotifier.UpdateSpendDetails(ntfn.OutPoint, details)
							return
						}
					}
				}
			}
		}

		// Haven't found the spender yet. Scan the next block.
		height++
	}

}

// RegisterConfirmationsNtfn registers a notification with DcrdNotifier
// which will be triggered once the txid reaches numConfs number of
// confirmations.
func (n *DcrdNotifier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
	pkScript []byte, numConfs, heightHint uint32) (*chainntnfs.ConfirmationEvent, error) {

	// Construct a notification request for the transaction and send it to
	// the main event loop.
	ntfn := &chainntnfs.ConfNtfn{
		ConfID:           atomic.AddUint64(&n.confClientCounter, 1),
		TxID:             txid,
		PkScript:         pkScript,
		NumConfirmations: numConfs,
		Event:            chainntnfs.NewConfirmationEvent(numConfs),
		HeightHint:       heightHint,
	}

	chainntnfs.Log.Infof("New confirmation subscription: "+
		"txid=%v, numconfs=%v", txid, numConfs)

	// Register the conf notification with the TxNotifier. A non-nil value
	// for `dispatch` will be returned if we are required to perform a
	// manual scan for the confirmation. Otherwise the notifier will begin
	// watching at tip for the transaction to confirm.
	dispatch, err := n.txNotifier.RegisterConf(ntfn)
	if err != nil {
		return nil, err
	}

	if dispatch == nil {
		return ntfn.Event, nil
	}

	select {
	case n.notificationRegistry <- dispatch:
		return ntfn.Event, nil
	case <-n.quit:
		return nil, ErrChainNotifierShuttingDown
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
