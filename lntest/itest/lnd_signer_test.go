package itest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/signrpc"
	"github.com/decred/dcrlnd/lnrpc/walletrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
)

// testDeriveSharedKey checks the ECDH performed by the endpoint
// DeriveSharedKey. It creates an ephemeral private key, performing an ECDH with
// the node's pubkey and a customized public key to check the validity of the
// result.
func testDeriveSharedKey(net *lntest.NetworkHarness, t *harnessTest) {
	runDeriveSharedKey(t, net.Alice)
}

// runDeriveSharedKey checks the ECDH performed by the endpoint
// DeriveSharedKey. It creates an ephemeral private key, performing an ECDH with
// the node's pubkey and a customized public key to check the validity of the
// result.
func runDeriveSharedKey(t *harnessTest, alice *lntest.HarnessNode) {
	ctxb := context.Background()

	// Create an ephemeral key, extracts its public key, and make a
	// PrivKeyECDH using the ephemeral key.
	ephemeralPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t.t, err, "failed to create ephemeral key")

	ephemeralPubBytes := ephemeralPriv.PubKey().SerializeCompressed()
	privKeyECDH := &keychain.PrivKeyECDH{PrivKey: ephemeralPriv}

	// assertECDHMatch checks the correctness of the ECDH between the
	// ephemeral key and the given public key.
	assertECDHMatch := func(pub *secp256k1.PublicKey,
		req *signrpc.SharedKeyRequest) {

		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		resp, err := alice.SignerClient.DeriveSharedKey(ctxt, req)
		require.NoError(t.t, err, "calling DeriveSharedKey failed")

		sharedKey, _ := privKeyECDH.ECDH(pub)
		require.Equal(
			t.t, sharedKey[:], resp.SharedKey,
			"failed to derive the expected key",
		)
	}

	nodePub, err := secp256k1.ParsePubKey(alice.PubKey[:])
	require.NoError(t.t, err, "failed to parse node pubkey")

	customizedKeyFamily := int32(keychain.KeyFamilyMultiSig)
	customizedIndex := int32(1)
	customizedPub, err := deriveCustomizedKey(
		ctxb, alice, customizedKeyFamily, customizedIndex,
	)
	require.NoError(t.t, err, "failed to create customized pubkey")

	// Test DeriveSharedKey with no optional arguments. It will result in
	// performing an ECDH between the ephemeral key and the node's pubkey.
	req := &signrpc.SharedKeyRequest{EphemeralPubkey: ephemeralPubBytes}
	assertECDHMatch(nodePub, req)

	// Test DeriveSharedKey with a KeyLoc which points to the node's pubkey.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(keychain.KeyFamilyNodeKey),
			KeyIndex:  0,
		},
	}
	assertECDHMatch(nodePub, req)

	// Test DeriveSharedKey with a KeyLoc being set in KeyDesc. The KeyLoc
	// points to the node's pubkey.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: int32(keychain.KeyFamilyNodeKey),
				KeyIndex:  0,
			},
		},
	}
	assertECDHMatch(nodePub, req)

	// Test DeriveSharedKey with RawKeyBytes set in KeyDesc. The RawKeyBytes
	// is the node's pubkey bytes, and the KeyFamily is KeyFamilyNodeKey.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			RawKeyBytes: alice.PubKey[:],
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: int32(keychain.KeyFamilyNodeKey),
			},
		},
	}
	assertECDHMatch(nodePub, req)

	// Test DeriveSharedKey with a KeyLoc which points to the customized
	// public key.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: customizedKeyFamily,
			KeyIndex:  customizedIndex,
		},
	}
	assertECDHMatch(customizedPub, req)

	// Test DeriveSharedKey with a KeyLoc being set in KeyDesc. The KeyLoc
	// points to the customized public key.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: customizedKeyFamily,
				KeyIndex:  customizedIndex,
			},
		},
	}
	assertECDHMatch(customizedPub, req)

	// Test DeriveSharedKey with RawKeyBytes set in KeyDesc. The RawKeyBytes
	// is the customized public key. The KeyLoc is also set with the family
	// being the customizedKeyFamily.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			RawKeyBytes: customizedPub.SerializeCompressed(),
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: customizedKeyFamily,
			},
		},
	}
	assertECDHMatch(customizedPub, req)

	// assertErrorMatch checks when calling DeriveSharedKey with invalid
	// params, the expected error is returned.
	assertErrorMatch := func(match string, req *signrpc.SharedKeyRequest) {
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		_, err := alice.SignerClient.DeriveSharedKey(ctxt, req)
		require.Error(t.t, err, "expected to have an error")
		require.Contains(
			t.t, err.Error(), match, "error failed to match",
		)
	}

	// Test that EphemeralPubkey must be supplied.
	req = &signrpc.SharedKeyRequest{}
	assertErrorMatch("must provide ephemeral pubkey", req)

	// Test that cannot use both KeyDesc and KeyLoc.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			RawKeyBytes: customizedPub.SerializeCompressed(),
		},
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: customizedKeyFamily,
			KeyIndex:  0,
		},
	}
	assertErrorMatch("use either key_desc or key_loc", req)

	// Test when KeyDesc is used, KeyLoc must be set.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			RawKeyBytes: alice.PubKey[:],
		},
	}
	assertErrorMatch("key_desc.key_loc must also be set", req)

	// Test that cannot use both RawKeyBytes and KeyIndex.
	req = &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubBytes,
		KeyDesc: &signrpc.KeyDescriptor{
			RawKeyBytes: customizedPub.SerializeCompressed(),
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: customizedKeyFamily,
				KeyIndex:  1,
			},
		},
	}
	assertErrorMatch("use either raw_key_bytes or key_index", req)
}

// testSignOutputRaw makes sure that the SignOutputRaw RPC can be used with all
// custom ways of specifying the signing key in the key descriptor/locator.
func testSignOutputRaw(net *lntest.NetworkHarness, t *harnessTest) {
	alice := net.NewNode(t.t, "alice", nil)
	defer shutdownAndAssert(net, t, alice)
	net.SendCoins(t.t, dcrutil.AtomsPerCoin, alice)
	runSignOutputRaw(t, net, alice)
}

// runSignOutputRaw makes sure that the SignOutputRaw RPC can be used with all
// custom ways of specifying the signing key in the key descriptor/locator.
func runSignOutputRaw(t *harnessTest, net *lntest.NetworkHarness,
	alice *lntest.HarnessNode) {

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()

	// For the next step, we need a public key. Let's use a special family
	// for this. We want this to be an index of zero.
	// Note(decred): this needs to be one of the known families because the
	// keychain pkg does not derive keys for other families.
	const testCustomKeyFamily = 1
	keyDesc, err := alice.WalletKitClient.DeriveNextKey(
		ctxt, &walletrpc.KeyReq{
			KeyFamily: testCustomKeyFamily,
		},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, int32(0), keyDesc.KeyLoc.KeyIndex)

	targetPubKey, err := secp256k1.ParsePubKey(keyDesc.RawKeyBytes)
	require.NoError(t.t, err)

	// First, try with a key descriptor that only sets the public key.
	assertSignOutputRaw(
		t, net, alice, targetPubKey, &signrpc.KeyDescriptor{
			RawKeyBytes: keyDesc.RawKeyBytes,
		},
	)

	// Now try again, this time only with the (0 index!) key locator.
	assertSignOutputRaw(
		t, net, alice, targetPubKey, &signrpc.KeyDescriptor{
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: keyDesc.KeyLoc.KeyFamily,
				KeyIndex:  keyDesc.KeyLoc.KeyIndex,
			},
		},
	)

	// And now test everything again with a new key where we know the index
	// is not 0.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	keyDesc, err = alice.WalletKitClient.DeriveNextKey(
		ctxt, &walletrpc.KeyReq{
			KeyFamily: testCustomKeyFamily,
		},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, int32(1), keyDesc.KeyLoc.KeyIndex)

	targetPubKey, err = secp256k1.ParsePubKey(keyDesc.RawKeyBytes)
	require.NoError(t.t, err)

	// First, try with a key descriptor that only sets the public key.
	assertSignOutputRaw(
		t, net, alice, targetPubKey, &signrpc.KeyDescriptor{
			RawKeyBytes: keyDesc.RawKeyBytes,
		},
	)

	// Now try again, this time only with the key locator.
	assertSignOutputRaw(
		t, net, alice, targetPubKey, &signrpc.KeyDescriptor{
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: keyDesc.KeyLoc.KeyFamily,
				KeyIndex:  keyDesc.KeyLoc.KeyIndex,
			},
		},
	)
}

// assertSignOutputRaw sends coins to a p2wkh address derived from the given
// target public key and then tries to spend that output again by invoking the
// SignOutputRaw RPC with the key descriptor provided.
func assertSignOutputRaw(t *harnessTest, net *lntest.NetworkHarness,
	alice *lntest.HarnessNode, targetPubKey *secp256k1.PublicKey,
	keyDesc *signrpc.KeyDescriptor) {

	ctxb := context.Background()
	ctxt, cancel := context.WithTimeout(ctxb, defaultTimeout*3)
	defer cancel()

	pubKeyHash := dcrutil.Hash160(targetPubKey.SerializeCompressed())
	targetAddr, err := stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(
		pubKeyHash, harnessNetParams,
	)
	require.NoError(t.t, err)
	_, targetScript := targetAddr.PaymentScript()

	// Send some coins to the generated p2wpkh address.
	_, err = alice.SendCoins(ctxt, &lnrpc.SendCoinsRequest{
		Addr:   targetAddr.String(),
		Amount: 800_000,
	})
	require.NoError(t.t, err)

	// Wait until the TX is found in the mempool.
	txid, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	require.NoError(t.t, err)

	targetOutputIndex := getOutputIndex(
		t, net.Miner, txid, targetAddr.String(),
	)

	// Clear the mempool.
	mineBlocks(t, net, 1, 1)

	// Try to spend the output now to a new p2pkh address.
	p2pkhResp, err := alice.NewAddress(ctxt, &lnrpc.NewAddressRequest{
		Type: lnrpc.AddressType_PUBKEY_HASH,
	})
	require.NoError(t.t, err)

	p2pkhAddr, err := stdaddr.DecodeAddress(
		p2pkhResp.Address, harnessNetParams,
	)
	require.NoError(t.t, err)

	scriptVersion, p2pkhPkScript := p2pkhAddr.PaymentScript()
	require.NoError(t.t, err)

	tx := wire.NewMsgTx()
	tx.Version = input.LNTxVersion
	tx.TxIn = []*wire.TxIn{{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *txid,
			Index: uint32(targetOutputIndex),
		},
	}}
	value := int64(800_000 - 3000)
	tx.TxOut = []*wire.TxOut{{
		Version:  scriptVersion,
		PkScript: p2pkhPkScript,
		Value:    value,
	}}

	var buf bytes.Buffer
	require.NoError(t.t, tx.Serialize(&buf))

	signResp, err := alice.SignerClient.SignOutputRaw(
		ctxt, &signrpc.SignReq{
			RawTxBytes: buf.Bytes(),
			SignDescs: []*signrpc.SignDescriptor{{
				Output: &signrpc.TxOut{
					PkScript: targetScript,
					Value:    800_000,
				},
				InputIndex:    0,
				KeyDesc:       keyDesc,
				Sighash:       uint32(txscript.SigHashAll),
				WitnessScript: targetScript,
			}},
		},
	)
	require.NoError(t.t, err)

	sigScript, err := input.WitnessStackToSigScript([][]byte{
		append(signResp.RawSigs[0], byte(txscript.SigHashAll)),
		targetPubKey.SerializeCompressed(),
	})
	require.NoError(t.t, err)
	tx.TxIn[0].SignatureScript = sigScript

	buf.Reset()
	require.NoError(t.t, tx.Serialize(&buf))

	_, err = alice.WalletKitClient.PublishTransaction(
		ctxt, &walletrpc.Transaction{
			TxHex: buf.Bytes(),
		},
	)
	require.NoError(t.t, err)

	// Wait until the spending tx is found.
	txid, err = waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	require.NoError(t.t, err)
	p2wkhOutputIndex := getOutputIndex(
		t, net.Miner, txid, p2pkhAddr.String(),
	)
	op := &lnrpc.OutPoint{
		TxidBytes:   txid[:],
		OutputIndex: uint32(p2wkhOutputIndex),
	}
	assertWalletUnspent(t, alice, op)

	// Mine another block to clean up the mempool and to make sure the spend
	// tx is actually included in a block.
	mineBlocks(t, net, 1, 1)
}

// deriveCustomizedKey uses the family and index to derive a public key from
// the node's walletkit client.
func deriveCustomizedKey(ctx context.Context, node *lntest.HarnessNode,
	family, index int32) (*secp256k1.PublicKey, error) {

	ctxt, _ := context.WithTimeout(ctx, defaultTimeout)
	req := &signrpc.KeyLocator{
		KeyFamily: family,
		KeyIndex:  index,
	}
	resp, err := node.WalletKitClient.DeriveKey(ctxt, req)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key: %v", err)
	}
	pub, err := secp256k1.ParsePubKey(resp.RawKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node pubkey: %v", err)
	}
	return pub, nil
}
