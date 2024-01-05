package csnotify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/queue"
)

var (
	// ErrChainNotifierShuttingDown is used when we are trying to
	// measure a spend notification when notifier is already stopped.
	ErrChainNotifierShuttingDown = errors.New("chainntnfs: system interrupt " +
		"while attempting to register for spend notification")
)

type ChainSource interface {
	GetBlock(context.Context, *chainhash.Hash) (*wire.MsgBlock, error)
	CurrentTip(context.Context) (*chainhash.Hash, int32, error)

	ChainEvents(context.Context) <-chan chainscan.ChainEvent

	GetCFilter(context.Context, int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error)

	GetBlockHash(context.Context, int32) (*chainhash.Hash, error)
	GetBlockHeader(context.Context, *chainhash.Hash) (*wire.BlockHeader, error)
	StoresReorgedHeaders() bool

	Run(context.Context) error
}

// chainConn adapts a ChainSource (plus a context) into a chainntf.ChainConn
type chainConn struct {
	c            ChainSource
	ctx          context.Context
	storesReorgs bool
}

func (c *chainConn) GetBlockHash(height int64) (*chainhash.Hash, error) {
	return c.c.GetBlockHash(c.ctx, int32(height))
}

func (c *chainConn) GetBlockHeader(hash *chainhash.Hash) (*wire.BlockHeader, error) {
	return c.c.GetBlockHeader(c.ctx, hash)
}

// ChainscanNotifier implements the ChainNotifier interface by using the
// chainscan package components.  Multiple concurrent clients are supported.
// All notifications are achieved via non-blocking sends on client channels.
//
// NOTE: This assumes for the moment the backing chain source is a dcrwallet
// instance (either embedded or remote) so it makes assumptions about the kinds
// of things the wallet stores and how it behaves.
type ChainscanNotifier struct {
	epochClientCounter uint64 // To be used atomically.

	start   sync.Once
	active  int32 // To be used atomically.
	stopped int32 // To be used atomically.

	ctx       context.Context
	cancelCtx func()

	tipTxsMtx     sync.Mutex
	tipWatcherTxs map[chainhash.Hash]map[*wire.MsgTx]chainscan.Event

	historical  *chainscan.Historical
	tipWatcher  *chainscan.TipWatcher
	chainConn   *chainConn
	chainEvents <-chan chainscan.ChainEvent

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

// Ensure ChainscanNotifier implements the ChainNotifier interface at compile time.
var _ chainntnfs.ChainNotifier = (*ChainscanNotifier)(nil)

// New returns a new ChainscanNotifier instance. This function assumes the dcrd node
// detailed in the passed configuration is already running, and willing to
// accept new websockets clients.
func New(chainSrc ChainSource,
	chainParams *chaincfg.Params, spendHintCache chainntnfs.SpendHintCache,
	confirmHintCache chainntnfs.ConfirmHintCache) (*ChainscanNotifier, error) {

	ctx, cancel := context.WithCancel(context.Background())
	chainConn := &chainConn{
		c:            chainSrc,
		ctx:          ctx,
		storesReorgs: chainSrc.StoresReorgedHeaders(),
	}

	historical := chainscan.NewHistorical(chainSrc)
	tipWatcher := chainscan.NewTipWatcher(chainSrc)

	notifier := &ChainscanNotifier{
		chainParams: chainParams,

		historical:    historical,
		tipWatcher:    tipWatcher,
		chainConn:     chainConn,
		tipWatcherTxs: make(map[chainhash.Hash]map[*wire.MsgTx]chainscan.Event),

		ctx:       ctx,
		cancelCtx: cancel,

		notificationCancels:  make(chan interface{}),
		notificationRegistry: make(chan interface{}),

		blockEpochClients: make(map[uint64]*blockEpochRegistration),

		chainUpdates: queue.NewConcurrentQueue(10),

		spendHintCache:   spendHintCache,
		confirmHintCache: confirmHintCache,

		quit: make(chan struct{}),
	}

	return notifier, nil
}

func runAndLogOnError(ctx context.Context, f func(context.Context) error, name string) {
	go func() {
		err := f(ctx)
		select {
		case <-ctx.Done():
			// Any errs were due to done() so, ok
			return
		default:
		}
		if err != nil {
			chainntnfs.Log.Errorf("CSNotify error while running %s: %v", name, err)
		}
	}()
}

// Start connects to the running dcrd node over websockets, registers for block
// notifications, and finally launches all related helper goroutines.
func (n *ChainscanNotifier) Start() error {
	var startErr error
	n.start.Do(func() {
		startErr = n.startNotifier()
	})
	return startErr
}

func (n *ChainscanNotifier) startNotifier() error {
	chainntnfs.Log.Infof("Starting chainscan notifier")

	runAndLogOnError(n.ctx, n.chainConn.c.Run, "chainConn")
	runAndLogOnError(n.ctx, n.tipWatcher.Run, "tipWatcher")
	runAndLogOnError(n.ctx, n.historical.Run, "historical")

	n.chainEvents = n.tipWatcher.ChainEvents(n.ctx)
	n.chainUpdates.Start()

	currentHash, currentHeight, err := n.chainConn.c.CurrentTip(n.ctx)
	if err != nil {
		n.chainUpdates.Stop()
		return err
	}

	currentHeader, err := n.chainConn.c.GetBlockHeader(n.ctx, currentHash)
	if err != nil {
		n.chainUpdates.Stop()
		return err
	}

	chainntnfs.Log.Debugf("Starting txnotifier at height %d hash %s",
		currentHeight, currentHash)

	n.txNotifier = chainntnfs.NewTxNotifier(
		uint32(currentHeight), chainntnfs.ReorgSafetyLimit,
		n.confirmHintCache, n.spendHintCache, n.chainParams,
	)

	n.bestBlock = chainntnfs.BlockEpoch{
		Height:      currentHeight,
		Hash:        currentHash,
		BlockHeader: currentHeader,
	}

	n.wg.Add(2)
	go n.notificationDispatcher()
	go n.handleChainEvents()

	// Set the active flag now that we've completed the full startup.
	atomic.StoreInt32(&n.active, 1)

	return nil
}

// Started returns true if this instance has been started, and false otherwise.
func (n *ChainscanNotifier) Started() bool {
	return atomic.LoadInt32(&n.active) != 0
}

// Stop shutsdown the ChainscanNotifier.
func (n *ChainscanNotifier) Stop() error {
	// Already shutting down?
	if atomic.AddInt32(&n.stopped, 1) != 1 {
		return nil
	}

	chainntnfs.Log.Debug("ChainscanNotifier shutting down")

	// Cancel any outstanding request.
	n.cancelCtx()

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

	chainntnfs.Log.Info("ChainscanNotifier shut down")

	return nil
}

// filteredBlock represents a new block which has been connected to the main
// chain. The slice of transactions will only be populated if the block
// includes a transaction that confirmed one of our watched txids, or spends
// one of the outputs currently being watched.
type filteredBlock struct {
	hash     *chainhash.Hash
	height   int32
	prevHash *chainhash.Hash
	header   *wire.BlockHeader

	txns []*dcrutil.Tx

	// connected is true if this update is a new block and false if it is a
	// disconnected block.
	connect bool
}

// foundAtTip is called by the tipWatcher whenever a watched target matches.
func (n *ChainscanNotifier) foundAtTip(e chainscan.Event, _ chainscan.FindFunc) {
	chainntnfs.Log.Tracef("Found at tip bh %s: %s", e.BlockHash, e)
	n.tipTxsMtx.Lock()
	txs, ok := n.tipWatcherTxs[e.BlockHash]
	if !ok {
		txs = make(map[*wire.MsgTx]chainscan.Event)
		n.tipWatcherTxs[e.BlockHash] = txs
	}
	txs[e.Tx] = e
	n.tipTxsMtx.Unlock()
}

func (n *ChainscanNotifier) drainTipWatcherTxs(blockHash *chainhash.Hash) []*dcrutil.Tx {
	n.tipTxsMtx.Lock()
	txs := n.tipWatcherTxs[*blockHash]
	utxs := make([]*dcrutil.Tx, 0, len(txs))
	for tx, etx := range txs {
		utx := dcrutil.NewTx(tx)
		utx.SetTree(etx.Tree)
		utx.SetIndex(int(etx.TxIndex))
		utxs = append(utxs, utx)
	}

	delete(n.tipWatcherTxs, *blockHash)
	n.tipTxsMtx.Unlock()
	return utxs
}

func (n *ChainscanNotifier) handleChainEvents() {
	defer n.wg.Done()

	for {
		var e chainscan.ChainEvent
		select {
		case <-n.ctx.Done():
			return
		case e = <-n.chainEvents:
		}

		fb := &filteredBlock{
			hash:     e.BlockHash(),
			height:   e.BlockHeight(),
			prevHash: e.PrevBlockHash(),
			header:   e.BlockHeader(),
		}

		if _, ok := e.(chainscan.BlockConnectedEvent); ok {
			fb.connect = true
			fb.txns = n.drainTipWatcherTxs(e.BlockHash())
		}

		select {
		case n.chainUpdates.ChanIn() <- fb:
		case <-n.ctx.Done():
			return
		}
	}
}

// notificationDispatcher is the primary goroutine which handles client
// notification registrations, as well as notification dispatches.
func (n *ChainscanNotifier) notificationDispatcher() {
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
						n.bestBlock.BlockHeader,
					)

					msg.errorChan <- nil
					continue
				}

				// Otherwise, we'll attempt to deliver the
				// backlog of notifications from their best
				// known block.
				missedBlocks, err := chainntnfs.GetClientMissedBlocks(
					n.chainConn, msg.bestBlock,
					n.bestBlock.Height, n.chainConn.storesReorgs,
				)
				if err != nil {
					msg.errorChan <- err
					continue
				}

				for _, block := range missedBlocks {
					n.notifyBlockEpochClient(
						msg, block.Height, block.Hash, block.BlockHeader,
					)
				}

				msg.errorChan <- nil
			}

		case item := <-n.chainUpdates.ChanOut():
			update := item.(*filteredBlock)
			if update.connect {
				if *update.prevHash != *n.bestBlock.Hash {
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
							update.height,
							n.chainConn.storesReorgs,
						)
					if err != nil {
						// Set the bestBlock here in case
						// a catch up partially completed.
						n.bestBlock = newBestBlock
						chainntnfs.Log.Error(err)
						continue
					}

					n.handleMissedBlocks(newBestBlock, missedBlocks)
				}

				if err := n.handleBlockConnected(update); err != nil {
					chainntnfs.Log.Error(err)
				}
				continue
			}

			if update.height != n.bestBlock.Height {
				chainntnfs.Log.Infof("Missed disconnected" +
					"blocks, attempting to catch up")
			}

			newBestBlock, err := chainntnfs.RewindChain(
				n.chainConn, n.txNotifier, n.bestBlock,
				update.height-1,
			)
			if err != nil {
				chainntnfs.Log.Errorf("Unable to rewind chain "+
					"from height %d to height %d: %v",
					n.bestBlock.Height, update.height-1, err)
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

func (n *ChainscanNotifier) handleMissedBlocks(newBestBlock chainntnfs.BlockEpoch, missed []chainntnfs.BlockEpoch) {
	// Track the previous block hash to fill in the data.
	prevHash := newBestBlock.Hash

	for _, m := range missed {
		bh, cfkey, filter, err := n.chainConn.c.GetCFilter(n.ctx, m.Height)
		if err != nil {
			return
		}

		if *bh != *m.Hash {
			chainntnfs.Log.Warnf("Missed block hash (%s) different than "+
				"mainchain block hash (%s)", m.Hash, bh)
		}

		header, err := n.chainConn.c.GetBlockHeader(n.ctx, bh)
		if err != nil {
			return
		}

		e := chainscan.BlockConnectedEvent{
			Height:   m.Height,
			Hash:     *bh,
			CFKey:    cfkey,
			Filter:   filter,
			PrevHash: *prevHash,
			Header:   header,
		}
		if err := n.tipWatcher.ForceRescan(n.ctx, &e); err != nil {
			return
		}

		fb := &filteredBlock{
			hash:     e.BlockHash(),
			height:   e.BlockHeight(),
			prevHash: prevHash,
			txns:     n.drainTipWatcherTxs(e.BlockHash()),
			header:   e.BlockHeader(),
		}
		prevHash = bh

		n.handleBlockConnected(fb)
	}
}

// handleBlockConnected applies a chain update for a new block. Any watched
// transactions included this block will processed to either send notifications
// now or after numConfirmations confs.
func (n *ChainscanNotifier) handleBlockConnected(newBlock *filteredBlock) error {
	// We'll then extend the txNotifier's height with the information of
	// this new block, which will handle all of the notification logic for
	// us.
	newBlockHash := newBlock.hash
	newBlockHeight := uint32(newBlock.height)
	err := n.txNotifier.ConnectTip(
		newBlockHash, newBlockHeight, newBlock.txns,
	)
	if err != nil {
		return fmt.Errorf("unable to connect tip: %v", err)
	}

	chainntnfs.Log.Infof("New block: height=%v, hash=%v, txs=%d", newBlockHeight,
		newBlockHash, len(newBlock.txns))

	// Now that we've guaranteed the new block extends the txNotifier's
	// current tip, we'll proceed to dispatch notifications to all of our
	// registered clients whom have had notifications fulfilled. Before
	// doing so, we'll make sure update our in memory state in order to
	// satisfy any client requests based upon the new block.
	n.bestBlock.Hash = newBlockHash
	n.bestBlock.Height = int32(newBlockHeight)
	n.bestBlock.BlockHeader = newBlock.header

	n.notifyBlockEpochs(int32(newBlockHeight), newBlockHash, newBlock.header)
	return n.txNotifier.NotifyHeight(newBlockHeight)
}

// notifyBlockEpochs notifies all registered block epoch clients of the newly
// connected block to the main chain.
func (n *ChainscanNotifier) notifyBlockEpochs(newHeight int32, newHash *chainhash.Hash,
	newHeader *wire.BlockHeader) {

	for _, client := range n.blockEpochClients {
		n.notifyBlockEpochClient(client, newHeight, newHash, newHeader)
	}
}

// notifyBlockEpochClient sends a registered block epoch client a notification
// about a specific block.
func (n *ChainscanNotifier) notifyBlockEpochClient(epochClient *blockEpochRegistration,
	height int32, hash *chainhash.Hash, header *wire.BlockHeader) {

	epoch := &chainntnfs.BlockEpoch{
		Height:      height,
		Hash:        hash,
		BlockHeader: header,
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
func (n *ChainscanNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {

	// Register the conf notification with the TxNotifier. A non-nil value
	// for `dispatch` will be returned if we are required to perform a
	// manual scan for the confirmation. Otherwise the notifier will begin
	// watching at tip for the transaction to confirm.
	ntfn, err := n.txNotifier.RegisterSpend(outpoint, pkScript, heightHint)
	if err != nil {
		return nil, err
	}

	// Normalize to zero outpoint so we don't trigger panics.
	if outpoint == nil {
		outpoint = &chainntnfs.ZeroOutPoint
	}

	// Set the TipWatcher to scan for spends of this output.
	var tipTarget, histTarget chainscan.Target
	scriptVersion := uint16(0)
	switch {
	case *outpoint == chainntnfs.ZeroOutPoint:
		tipTarget = chainscan.SpentScript(scriptVersion, pkScript)
		histTarget = chainscan.SpentScript(scriptVersion, pkScript)
	default:
		tipTarget = chainscan.SpentOutPoint(*outpoint, scriptVersion, pkScript)
		histTarget = chainscan.SpentOutPoint(*outpoint, scriptVersion, pkScript)
	}

	startHeightChan := make(chan int32)

	// TODO: handle cancelling this target after it is found and the
	// txnotifier's reorgSafetyLimit has been reached.
	n.tipWatcher.Find(
		tipTarget,
		chainscan.WithFoundCallback(n.foundAtTip),
		chainscan.WithStartWatchHeightChan(startHeightChan),
		// TODO: Add and verify safety of using
		// WithStartHeight(ntfn.Height)
	)

	// Determine when the TipWatcher actually started watching for this tx.
	var startWatchHeight int32
	select {
	case <-n.ctx.Done():
		return nil, n.ctx.Err()
	case startWatchHeight = <-startHeightChan:
	}

	// We can only exit early if the TipWatcher and the TxNotifier were in
	// sync. Otherwise we might need to force a historical search to
	// prevent any missed txs.
	if ntfn.HistoricalDispatch == nil && uint32(startWatchHeight) <= ntfn.Height {
		return ntfn.Event, nil
	}

	if ntfn.HistoricalDispatch == nil {
		// Ignore the error since problems would have been returned by
		// RegisterSpend().
		spendReq, _ := chainntnfs.NewSpendRequest(outpoint, pkScript)

		// The TipWatcher and txnotifier were out of sync, so even
		// though originally a historical search was not needed, we
		// force one to ensure there are no gaps in our search history.
		ntfn.HistoricalDispatch = &chainntnfs.HistoricalSpendDispatch{
			SpendRequest: spendReq,
			StartHeight:  ntfn.Height,
			EndHeight:    uint32(startWatchHeight),
		}
		chainntnfs.Log.Infof("Forcing historical search for %s between %d and %d",
			spendReq, ntfn.HistoricalDispatch.StartHeight,
			ntfn.HistoricalDispatch.EndHeight)
	} else if uint32(startWatchHeight) > ntfn.HistoricalDispatch.EndHeight {
		// We started watching after the txnotifier's currentHeight, so
		// update the historical search to include the extra blocks.
		chainntnfs.Log.Infof("Modifying historical search EndHeight "+
			"for %s from %d to %d", ntfn.HistoricalDispatch.SpendRequest,
			ntfn.HistoricalDispatch.EndHeight,
			startWatchHeight)
		ntfn.HistoricalDispatch.EndHeight = uint32(startWatchHeight)
	}

	// Handle a historical scan in a goroutine.
	go func() {
		var details *chainntnfs.SpendDetail
		completeChan := make(chan struct{})
		cancelChan := make(chan struct{})
		foundCb := func(e chainscan.Event, _ chainscan.FindFunc) {
			details = &chainntnfs.SpendDetail{
				SpentOutPoint:     outpoint,
				SpenderTxHash:     e.Tx.CachedTxHash(),
				SpendingTx:        e.Tx,
				SpenderInputIndex: uint32(e.Index),
				SpendingHeight:    e.BlockHeight,
			}
			close(cancelChan)
		}
		hist := ntfn.HistoricalDispatch
		n.historical.Find(
			histTarget,
			chainscan.WithFoundCallback(foundCb),
			chainscan.WithCompleteChan(completeChan),
			chainscan.WithCancelChan(cancelChan),
			chainscan.WithStartHeight(int32(hist.StartHeight)),
			chainscan.WithEndHeight(int32(hist.EndHeight)),
			// TODO: force to notify during the historical only if
			// the confirmation was approved or on the last block?
			// This is to handle
			// https://github.com/decred/dcrlnd/issues/69
		)

		select {
		case <-n.quit:
			close(cancelChan)
			return
		case <-completeChan:
		case <-cancelChan:
		}

		// We will invoke UpdateConfDetails even if none were found.
		// This allows the notifier to begin safely updating the height
		// hint cache at tip, since any pending rescans have now
		// completed.
		err = n.txNotifier.UpdateSpendDetails(hist.SpendRequest, details)
		if err != nil {
			chainntnfs.Log.Error(err)
		}
	}()

	return ntfn.Event, nil
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
func (n *ChainscanNotifier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
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

	// Set the TipWatcher to scan for this confirmation.
	var tipTarget, histTarget chainscan.Target
	scriptVersion := uint16(0)
	switch {
	case txid == nil || *txid == chainntnfs.ZeroHash:
		tipTarget = chainscan.ConfirmedScript(scriptVersion, pkScript)
		histTarget = chainscan.ConfirmedScript(scriptVersion, pkScript)
	default:
		tipTarget = chainscan.ConfirmedTransaction(*txid, scriptVersion, pkScript)
		histTarget = chainscan.ConfirmedTransaction(*txid, scriptVersion, pkScript)
	}

	startHeightChan := make(chan int32)

	// TODO: handle cancelling this target after it is found and the
	// txnotifier's reorgSafetyLimit has been reached.
	n.tipWatcher.Find(
		tipTarget,
		chainscan.WithFoundCallback(n.foundAtTip),
		chainscan.WithStartWatchHeightChan(startHeightChan),
		// TODO: Add and verify safety of using
		// WithStartHeight(ntfn.Height)
	)

	// Determine when the TipWatcher actually started watching for this tx.
	var startWatchHeight int32
	select {
	case <-n.ctx.Done():
		return nil, n.ctx.Err()
	case startWatchHeight = <-startHeightChan:
	}

	// We can only exit early if the TipWatcher and the TxNotifier were in
	// sync. Otherwise we might need to force a historical search to
	// prevent any missed txs.
	if ntfn.HistoricalDispatch == nil && uint32(startWatchHeight) <= ntfn.Height {
		return ntfn.Event, nil
	}

	if ntfn.HistoricalDispatch == nil {
		// Ignore errors since they would've been triggered by
		// RegisterConf() above.
		confReq, _ := chainntnfs.NewConfRequest(txid, pkScript)

		// The TipWatcher and txnotifier were out of sync, so even
		// though originally a historical search was not needed, we
		// force one to ensure there are no gaps in our search history.
		ntfn.HistoricalDispatch = &chainntnfs.HistoricalConfDispatch{
			ConfRequest: confReq,
			StartHeight: ntfn.Height,
			EndHeight:   uint32(startWatchHeight),
		}
		chainntnfs.Log.Infof("Forcing historical search for %s between %d and %d",
			confReq, ntfn.HistoricalDispatch.StartHeight,
			ntfn.HistoricalDispatch.EndHeight)
	} else if uint32(startWatchHeight) > ntfn.HistoricalDispatch.EndHeight {
		// We started watching after the txnotifier's currentHeight, so
		// update the historical search to include the extra blocks.
		chainntnfs.Log.Infof("Modifying historical search EndHeight "+
			"for %s from %d to %d", ntfn.HistoricalDispatch.ConfRequest,
			ntfn.HistoricalDispatch.EndHeight,
			startWatchHeight)
		ntfn.HistoricalDispatch.EndHeight = uint32(startWatchHeight)
	}

	// Handle a historical scan in a goroutine.
	go func() {
		var txconf *chainntnfs.TxConfirmation
		completeChan := make(chan struct{})
		cancelChan := make(chan struct{})
		foundCb := func(e chainscan.Event, _ chainscan.FindFunc) {
			txconf = &chainntnfs.TxConfirmation{
				Tx:          e.Tx,
				BlockHash:   &e.BlockHash,
				BlockHeight: uint32(e.BlockHeight),
				TxIndex:     uint32(e.TxIndex),
			}
			close(cancelChan)
		}
		hist := ntfn.HistoricalDispatch
		n.historical.Find(
			histTarget,
			chainscan.WithFoundCallback(foundCb),
			chainscan.WithCompleteChan(completeChan),
			chainscan.WithCancelChan(cancelChan),
			chainscan.WithStartHeight(int32(hist.StartHeight)),
			chainscan.WithEndHeight(int32(hist.EndHeight)),
			// TODO: force to notify during the historical only if
			// the confirmation was approved or on the last block?
			// This is to handle
			// https://github.com/decred/dcrlnd/issues/69
		)

		select {
		case <-n.quit:
			close(cancelChan)
			return
		case <-cancelChan:
		case <-completeChan:
		}

		// We will invoke UpdateConfDetails even if none were found.
		// This allows the notifier to begin safely updating the height
		// hint cache at tip, since any pending rescans have now
		// completed.
		err := n.txNotifier.UpdateConfDetails(
			hist.ConfRequest, txconf,
		)
		if err != nil {
			chainntnfs.Log.Error(err)
		}
	}()

	return ntfn.Event, nil
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

// epochCancel is a message sent to the ChainscanNotifier when a client wishes to
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
func (n *ChainscanNotifier) RegisterBlockEpochNtfn(
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
