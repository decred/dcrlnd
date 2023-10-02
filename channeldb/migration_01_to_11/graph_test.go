package migration_01_to_11

import (
	"encoding/hex"
	"image/color"
	prand "math/rand"
	"net"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	lnwire "github.com/decred/dcrlnd/channeldb/migration/lnwire21"
)

func modNScalar(b []byte) *secp256k1.ModNScalar {
	var m secp256k1.ModNScalar
	m.SetByteSlice(b)
	return &m
}

var (
	testAddr = &net.TCPAddr{IP: (net.IP)([]byte{0xA, 0x0, 0x0, 0x1}),
		Port: 9000}
	anotherAddr, _ = net.ResolveTCPAddr("tcp",
		"[2001:db8:85a3:0:0:8a2e:370:7334]:80")
	testAddrs = []net.Addr{testAddr, anotherAddr}
	rBytes, _ = hex.DecodeString("63724406601629180062774974542967536251589935445068131219452686511677818569431")
	sBytes, _ = hex.DecodeString("18801056069249825825291287104931333862866033135609736119018462340006816851118")
	testSig   = ecdsa.NewSignature(
		modNScalar(rBytes),
		modNScalar(sBytes),
	)

	testFeatures = lnwire.NewFeatureVector(nil, nil)
)

func createLightningNode(db *DB, priv *secp256k1.PrivateKey) (*LightningNode, error) {
	updateTime := prand.Int63()

	pub := priv.PubKey().SerializeCompressed()
	n := &LightningNode{
		HaveNodeAnnouncement: true,
		AuthSigBytes:         testSig.Serialize(),
		LastUpdate:           time.Unix(updateTime, 0),
		Color:                color.RGBA{1, 2, 3, 0},
		Alias:                "kek" + string(pub),
		Features:             testFeatures,
		Addresses:            testAddrs,
		db:                   db,
	}
	copy(n.PubKeyBytes[:], pub)

	return n, nil
}

func createTestVertex(db *DB) (*LightningNode, error) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	return createLightningNode(db, priv)
}
