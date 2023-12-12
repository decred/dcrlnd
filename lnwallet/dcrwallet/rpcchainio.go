package dcrwallet

import (
	"context"
	"encoding/hex"
	"sync"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/lnwallet"

	"decred.org/dcrwallet/v4/errors"
)

var (
	// ErrOutputSpent is returned by the GetUtxo method if the target output
	// for lookup has already been spent.
	ErrOutputSpent = errors.New("target output has been spent")

	// ErrUnconnected is returned when an IO operation was requested by the
	// backend is not connected to the network.
	//
	// TODO(decred) this should probably be exported by lnwallet and
	// expected by the BlockChainIO interface.
	ErrUnconnected = errors.New("unconnected to the network")
)

// RPCChainIO implements the required methods for performing chain io services.
type RPCChainIO struct {
	net        *chaincfg.Params
	blockCache *blockcache.BlockCache

	// mu is a mutex that protects the chain field.
	mu    sync.Mutex
	chain *rpcclient.Client
}

// Compile time check to ensure RPCChainIO fulfills lnwallet.BlockChainIO.
var _ lnwallet.BlockChainIO = (*RPCChainIO)(nil)

// NewRPCChainIO initializes a new blockchain IO implementation backed by a
// full dcrd node.  It requires the config for reaching the dcrd instance and
// the corresponding network this instance should be in.
func NewRPCChainIO(rpcConfig rpcclient.ConnConfig, net *chaincfg.Params,
	blockCache *blockcache.BlockCache) (*RPCChainIO, error) {
	// TODO: bring back this. rpcclient/v6 changed the semantics of the
	// context passed to Connect().
	/*
		connectTimeout := 30 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
	*/
	ctx := context.Background()

	rpcConfig.DisableConnectOnNew = true
	rpcConfig.DisableAutoReconnect = false
	chain, err := rpcclient.New(&rpcConfig, nil)
	if err != nil {
		return nil, err
	}

	// Try to connect to the given node.
	if err := chain.Connect(ctx, true); err != nil {
		return nil, err
	}

	return &RPCChainIO{
		net:        net,
		chain:      chain,
		blockCache: blockCache,
	}, nil
}

// GetBestBlock returns the current height and hash of the best known block
// within the main chain.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (s *RPCChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chain == nil {
		return nil, 0, ErrUnconnected
	}
	hash, height, err := s.chain.GetBestBlock(context.TODO())
	return hash, int32(height), err
}

// GetUtxo returns the original output referenced by the passed outpoint that
// create the target pkScript.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (s *RPCChainIO) GetUtxo(op *wire.OutPoint, pkScript []byte,
	heightHint uint32, cancel <-chan struct{}) (*wire.TxOut, error) {

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chain == nil {
		return nil, ErrUnconnected
	}

	txout, err := s.chain.GetTxOut(context.TODO(), &op.Hash, op.Index, op.Tree, false)
	if err != nil {
		return nil, err
	} else if txout == nil {
		return nil, ErrOutputSpent
	}

	pkScript, err = hex.DecodeString(txout.ScriptPubKey.Hex)
	if err != nil {
		return nil, err
	}

	// Sadly, gettxout returns the output value in DCR instead of atoms.
	amt, err := dcrutil.NewAmount(txout.Value)
	if err != nil {
		return nil, err
	}

	return &wire.TxOut{
		Value:    int64(amt),
		PkScript: pkScript,
	}, nil
}

// GetBlock returns a raw block from the server given its hash.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (s *RPCChainIO) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return s.blockCache.GetBlock(context.TODO(), blockHash, s.getBlock)
}

func (s *RPCChainIO) getBlock(ctx context.Context, blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chain == nil {
		return nil, ErrUnconnected
	}
	return s.chain.GetBlock(context.TODO(), blockHash)
}

// GetBlockHash returns the hash of the block in the best blockchain at the
// given height.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (s *RPCChainIO) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chain == nil {
		return nil, ErrUnconnected
	}
	return s.chain.GetBlockHash(context.TODO(), blockHeight)
}
