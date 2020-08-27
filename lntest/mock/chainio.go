package mock

import (
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
)

// ChainIO is a mock implementation of the BlockChainIO interface.
type ChainIO struct {
	BestHeight int32
}

// GetBestBlock currently returns dummy values.
func (c *ChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	return &chainhash.Hash{31: 0x01}, c.BestHeight, nil
}

// GetUtxo currently returns dummy values.
func (c *ChainIO) GetUtxo(op *wire.OutPoint, _ []byte,
	heightHint uint32, _ <-chan struct{}) (*wire.TxOut, error) {

	return nil, nil
}

// GetBlockHash currently returns dummy values.
func (c *ChainIO) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	return nil, nil
}

// GetBlock currently returns dummy values.
func (c *ChainIO) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return nil, nil
}
