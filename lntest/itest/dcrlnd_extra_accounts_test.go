package itest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/walletrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntest/wait"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/stretchr/testify/require"
)

func testExtraAccountsFeatures(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	if net.Alice.Cfg.RemoteWallet {
		t.t.Skipf("Disabled in remotewallet impl")
	}

	// Create a Carol node to use in tests.
	password := []byte("12345678")
	carol, mnemonic, _, err := net.NewNodeWithSeed(
		"carol", nil, password, false,
	)
	require.NoError(t.t, err)

	// Helper to check if Carol has balance.
	checkBalance := func(node *lntest.HarnessNode, wantBalance dcrutil.Amount) {
		t.t.Helper()
		require.NoError(t.t, wait.NoError(func() error {
			ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
			bal, err := node.WalletBalance(ctxt, &lnrpc.WalletBalanceRequest{})
			if err != nil {
				return err
			}

			if bal.ConfirmedBalance != int64(wantBalance) {
				return fmt.Errorf("ConfirmedBalance %d != %f",
					bal.ConfirmedBalance, dcrutil.AtomsPerCoin)
			}

			return nil
		}, defaultTimeout))
	}

	// Create an additional account for carol.
	accountName := "second"
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	_, err = carol.WalletKitClient.DeriveNextAccount(ctxt, &walletrpc.DeriveNextAccountRequest{Name: accountName})
	require.NoError(t.t, err)
	accounts, err := carol.WalletKitClient.ListAccounts(ctxb, &walletrpc.ListAccountsRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, accounts.Accounts, 2)

	// Get an address from this account and send funds to it.
	addrRes, err := carol.WalletKitClient.NextAddr(ctxt, &walletrpc.AddrRequest{Account: accountName})
	require.NoError(t.t, err)

	// Send coins to Alice, then from Alice to Carol.
	net.SendCoins(t.t, 2*dcrutil.AtomsPerCoin, net.Alice)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	_, err = net.Alice.SendCoins(ctxt, &lnrpc.SendCoinsRequest{Addr: addrRes.Addr, Amount: dcrutil.AtomsPerCoin})
	require.NoError(t.t, err)
	mineBlocks(t, net, 1, 1)
	checkBalance(carol, dcrutil.AtomsPerCoin)
	listUnspentReq := &walletrpc.ListUnspentRequest{Account: accountName, MaxConfs: math.MaxInt32}
	unspent, err := carol.WalletKitClient.ListUnspent(ctxb, listUnspentReq)
	require.NoError(t.t, err)
	require.Len(t.t, unspent.Utxos, 1)

	// Stop and recreate carol, to see if the new account will be
	// discovered.
	_, err = net.SuspendNode(carol)
	require.NoError(t.t, err)
	carol, err = net.RestoreNodeWithSeed(
		"carol", nil, password, mnemonic, 1000, nil,
	)
	defer shutdownAndAssert(net, t, carol)
	require.NoError(t.t, err)
	checkBalance(carol, dcrutil.AtomsPerCoin)
	accounts, err = carol.WalletKitClient.ListAccounts(ctxb, &walletrpc.ListAccountsRequest{})
	require.NoError(t.t, err)
	require.Len(t.t, accounts.Accounts, 2)

	// Gather the required data to build the SpendUTXOs request.
	_, minerHeight, err := net.Miner.Node.GetBestBlock(ctxt)
	require.NoError(t.t, err)
	utxo := unspent.Utxos[0]
	key, err := carol.WalletKitClient.ExportPrivateKey(ctxb, &walletrpc.ExportPrivateKeyRequest{Address: utxo.Address})
	require.NoError(t.t, err)

	// Create Dave.
	dave := net.NewNode(t.t, "dave", nil)
	defer shutdownAndAssert(net, t, dave)

	// Dave will spend the utxo from Carol's account into its own wallet.
	spendReq := &walletrpc.SpendUTXOsRequest{
		Utxos: []*walletrpc.SpendUTXOsRequest_UTXOAndKey{{
			Txid:          utxo.Outpoint.TxidBytes,
			Index:         utxo.Outpoint.OutputIndex,
			PrivateKeyWif: key.Wif,
			HeightHint:    uint32(minerHeight - utxo.Confirmations - 1),
			Address:       utxo.Address,
		}},
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	spendRes, err := dave.WalletKitClient.SpendUTXOs(ctxt, spendReq)
	require.NoError(t.t, err)

	// Check that Dave sees the tx.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	getWtxReq := &walletrpc.GetWalletTxRequest{Txid: spendRes.Txid}
	unminedTx, err := dave.WalletKitClient.GetWalletTx(ctxt, getWtxReq)
	require.NoError(t.t, err)
	require.Equal(t.t, int32(0), unminedTx.Confirmations)

	// Mine the tx.
	minedBlock := mineBlocks(t, net, 1, 1)[0]
	minedBh := minedBlock.BlockHash()

	// Dave now sees the mined tx.
	time.Sleep(100 * time.Millisecond)
	minedTx, err := dave.WalletKitClient.GetWalletTx(ctxt, getWtxReq)
	require.NoError(t.t, err)
	require.Equal(t.t, int32(1), minedTx.Confirmations)
	require.Equal(t.t, minedBh[:], minedTx.BlockHash)

	// Carol has no more balance and Dave has the balance minus tx fee.
	txSize := (&input.TxSizeEstimator{}).AddP2PKHInput().AddP2PKHOutput().Size()
	txFee := chainfee.AtomPerKByte(10000).FeeForSize(txSize)
	checkBalance(carol, 0)
	checkBalance(dave, dcrutil.AtomsPerCoin-txFee)

	// Dave can spend these coins.
	_, err = dave.SendCoins(ctxt, &lnrpc.SendCoinsRequest{Addr: addrRes.Addr, SendAll: true})
	require.NoError(t.t, err)
	mineBlocks(t, net, 1, 1)
	checkBalance(dave, 0)
	checkBalance(carol, dcrutil.AtomsPerCoin-txFee*2)
}
