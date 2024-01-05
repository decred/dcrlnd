//go:build dev
// +build dev

package csnotify

import (
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrlnd/chainntnfs"
)

// TODO(decred): Determine if the generateBlocks is needed.
//
// UnsafeStart starts the notifier with a specified best height and optional
// best hash. Its bestBlock and txNotifier are initialized with bestHeight and
// optionally bestHash. The parameter generateBlocks is necessary for the
// dcrd notifier to ensure we drain all notifications up to syncHeight,
// since if they are generated ahead of UnsafeStart the chainConn may start up
// with an outdated best block and miss sending ntfns. Used for testing.
func (n *ChainscanNotifier) UnsafeStart(bestHeight int64, bestHash *chainhash.Hash,
	syncHeight int64, generateBlocks func() error) error {

	n.txNotifier = chainntnfs.NewTxNotifier(
		uint32(bestHeight), chainntnfs.ReorgSafetyLimit,
		n.confirmHintCache, n.spendHintCache, n.chainParams,
	)

	runAndLogOnError(n.ctx, n.chainConn.c.Run, "chainConn")
	runAndLogOnError(n.ctx, n.tipWatcher.Run, "tipWatcher")
	runAndLogOnError(n.ctx, n.historical.Run, "historical")

	n.chainEvents = n.tipWatcher.ChainEvents(n.ctx)

	n.wg.Add(1)
	go n.handleChainEvents()

	if generateBlocks != nil {
		// Ensure no block notifications are pending when we start the
		// notification dispatcher goroutine.

		// First generate the blocks, then drain the notifications
		// for the generated blocks.
		if err := generateBlocks(); err != nil {
			return err
		}

		timeout := time.After(60 * time.Second)
	loop:
		for {
			select {
			case ntfn := <-n.chainUpdates:
				if int64(ntfn.height) >= syncHeight {
					break loop
				}
			case <-timeout:
				return fmt.Errorf("unable to catch up to height %d",
					syncHeight)
			}
		}
	}

	// Run notificationDispatcher after setting the notifier's best block
	// to avoid a race condition.
	n.bestBlock = chainntnfs.BlockEpoch{
		Height: int32(bestHeight), Hash: bestHash,
	}
	if bestHash == nil {
		hash, err := n.chainConn.GetBlockHash(int64(bestHeight))
		if err != nil {
			return err
		}
		n.bestBlock.Hash = hash
	}

	n.wg.Add(1)
	go n.notificationDispatcher()

	return nil
}
