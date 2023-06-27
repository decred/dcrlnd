package lnwallet

import (
	"errors"
	"testing"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
	"github.com/stretchr/testify/require"
)

func TestErrUtxoAlreadySpent(t *testing.T) {
	err1 := ErrUtxoAlreadySpent{
		PrevOutPoint:     wire.OutPoint{Hash: chainhash.Hash{0: 0x01}, Index: 1},
		SpendingOutPoint: wire.OutPoint{Hash: chainhash.Hash{0: 0x02}, Index: 2},
	}
	require.True(t, errors.Is(err1, ErrUtxoAlreadySpent{}))
	var err2 ErrUtxoAlreadySpent
	require.True(t, errors.As(err1, &err2))
	require.Equal(t, err1, err2)

}
