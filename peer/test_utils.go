package peer

import (
	"bytes"
	crand "crypto/rand"
	"encoding/binary"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/htlcswitch"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lntest/channels"
	"github.com/decred/dcrlnd/lntest/mock"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/netann"
	"github.com/decred/dcrlnd/queue"
	"github.com/decred/dcrlnd/shachain"
	"github.com/stretchr/testify/require"
)

const (
	broadcastHeight = 100

	// timeout is a timeout value to use for tests which need to wait for
	// a return value on a channel.
	timeout = time.Second * 5
)

var (
	// Just use some arbitrary bytes as delivery script.
	dummyDeliveryScript = channels.AlicesPrivKey
)

// noUpdate is a function which can be used as a parameter in createTestPeer to
// call the setup code with no custom values on the channels set up.
var noUpdate = func(a, b *channeldb.OpenChannel) {}

// createTestPeer creates a channel between two nodes, and returns a peer for
// one of the nodes, together with the channel seen from both nodes. It takes
// an updateChan function which can be used to modify the default values on
// the channel states for each peer.
func createTestPeer(notifier chainntnfs.ChainNotifier,
	publTx chan *wire.MsgTx, updateChan func(a, b *channeldb.OpenChannel),
	mockSwitch *mockMessageSwitch) (
	*Brontide, *lnwallet.LightningChannel, func(), error) {

	chainParams := chaincfg.RegNetParams()

	aliceKeyPriv, aliceKeyPub := channels.PrivKeyFromBytes(channels.AlicesPrivKey)
	aliceKeySigner := &keychain.PrivKeyDigestSigner{PrivKey: aliceKeyPriv}
	bobKeyPriv, bobKeyPub := channels.PrivKeyFromBytes(channels.BobsPrivKey)

	channelCapacity := dcrutil.Amount(10 * 1e8)
	channelBal := channelCapacity / 2
	aliceDustLimit := dcrutil.Amount(200)
	bobDustLimit := dcrutil.Amount(1300)
	csvTimeoutAlice := uint32(5)
	csvTimeoutBob := uint32(4)

	prevOut := &wire.OutPoint{
		Hash:  channels.TestHdSeed,
		Index: 0,
	}
	fundingTxIn := wire.NewTxIn(prevOut, 0, nil) // TODO(decred): Need correct input value

	aliceCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit:        aliceDustLimit,
			MaxPendingAmount: lnwire.MilliAtom(rand.Int63()),
			ChanReserve:      dcrutil.Amount(rand.Int63()),
			MinHTLC:          lnwire.MilliAtom(rand.Int63()),
			MaxAcceptedHtlcs: uint16(rand.Int31()),
			CsvDelay:         uint16(csvTimeoutAlice),
		},
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
	}
	bobCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit:        bobDustLimit,
			MaxPendingAmount: lnwire.MilliAtom(rand.Int63()),
			ChanReserve:      dcrutil.Amount(rand.Int63()),
			MinHTLC:          lnwire.MilliAtom(rand.Int63()),
			MaxAcceptedHtlcs: uint16(rand.Int31()),
			CsvDelay:         uint16(csvTimeoutBob),
		},
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
	}

	bobRoot, err := chainhash.NewHash(bobKeyPriv.Serialize())
	if err != nil {
		return nil, nil, nil, err
	}
	bobPreimageProducer := shachain.NewRevocationProducer(shachain.ShaHash(*bobRoot))
	bobFirstRevoke, err := bobPreimageProducer.AtIndex(0)
	if err != nil {
		return nil, nil, nil, err
	}
	bobCommitPoint := input.ComputeCommitmentPoint(bobFirstRevoke[:])

	aliceRoot, err := chainhash.NewHash(aliceKeyPriv.Serialize())
	if err != nil {
		return nil, nil, nil, err
	}
	alicePreimageProducer := shachain.NewRevocationProducer(shachain.ShaHash(*aliceRoot))
	aliceFirstRevoke, err := alicePreimageProducer.AtIndex(0)
	if err != nil {
		return nil, nil, nil, err
	}
	aliceCommitPoint := input.ComputeCommitmentPoint(aliceFirstRevoke[:])

	aliceCommitTx, bobCommitTx, err := lnwallet.CreateCommitmentTxns(
		channelBal, channelBal, &aliceCfg, &bobCfg, aliceCommitPoint,
		bobCommitPoint, *fundingTxIn, channeldb.SingleFunderTweaklessBit,
		chainParams,
	)
	if err != nil {
		return nil, nil, nil, err
	}

	alicePath, err := ioutil.TempDir("", "alicedb")
	if err != nil {
		return nil, nil, nil, err
	}

	dbAlice, err := channeldb.Open(alicePath)
	if err != nil {
		return nil, nil, nil, err
	}

	bobPath, err := ioutil.TempDir("", "bobdb")
	if err != nil {
		return nil, nil, nil, err
	}

	dbBob, err := channeldb.Open(bobPath)
	if err != nil {
		return nil, nil, nil, err
	}

	estimator := chainfee.NewStaticEstimator(12500, 0)
	feePerKB, err := estimator.EstimateFeePerKB(1)
	if err != nil {
		return nil, nil, nil, err
	}

	// TODO(roasbeef): need to factor in commit fee?
	aliceCommit := channeldb.ChannelCommitment{
		CommitHeight:  0,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(channelBal),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(channelBal),
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitFee:     feePerKB.FeeForSize(input.CommitmentTxSize),
		CommitTx:      aliceCommitTx,
		CommitSig:     bytes.Repeat([]byte{1}, 71),
	}
	bobCommit := channeldb.ChannelCommitment{
		CommitHeight:  0,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(channelBal),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(channelBal),
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitFee:     feePerKB.FeeForSize(input.CommitmentTxSize),
		CommitTx:      bobCommitTx,
		CommitSig:     bytes.Repeat([]byte{1}, 71),
	}

	var chanIDBytes [8]byte
	if _, err := io.ReadFull(crand.Reader, chanIDBytes[:]); err != nil {
		return nil, nil, nil, err
	}

	shortChanID := lnwire.NewShortChanIDFromInt(
		binary.BigEndian.Uint64(chanIDBytes[:]),
	)

	aliceChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            aliceCfg,
		RemoteChanCfg:           bobCfg,
		IdentityPub:             aliceKeyPub,
		FundingOutpoint:         *prevOut,
		ShortChannelID:          shortChanID,
		ChanType:                channeldb.SingleFunderTweaklessBit,
		IsInitiator:             true,
		Capacity:                channelCapacity,
		RemoteCurrentRevocation: bobCommitPoint,
		RevocationProducer:      alicePreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         aliceCommit,
		RemoteCommitment:        aliceCommit,
		Db:                      dbAlice.ChannelStateDB(),
		Packager:                channeldb.NewChannelPackager(shortChanID),
		FundingTxn:              channels.TestFundingTx,
	}
	bobChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            bobCfg,
		RemoteChanCfg:           aliceCfg,
		IdentityPub:             bobKeyPub,
		FundingOutpoint:         *prevOut,
		ChanType:                channeldb.SingleFunderTweaklessBit,
		IsInitiator:             false,
		Capacity:                channelCapacity,
		RemoteCurrentRevocation: aliceCommitPoint,
		RevocationProducer:      bobPreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         bobCommit,
		RemoteCommitment:        bobCommit,
		Db:                      dbBob.ChannelStateDB(),
		Packager:                channeldb.NewChannelPackager(shortChanID),
	}

	// Set custom values on the channel states.
	updateChan(aliceChannelState, bobChannelState)

	aliceAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 18555,
	}

	if err := aliceChannelState.SyncPending(aliceAddr, 0); err != nil {
		return nil, nil, nil, err
	}

	bobAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 18556,
	}

	if err := bobChannelState.SyncPending(bobAddr, 0); err != nil {
		return nil, nil, nil, err
	}

	cleanUpFunc := func() {
		os.RemoveAll(bobPath)
		os.RemoveAll(alicePath)
	}

	aliceSigner := &mock.SingleSigner{Privkey: aliceKeyPriv}
	bobSigner := &mock.SingleSigner{Privkey: bobKeyPriv}

	alicePool := lnwallet.NewSigPool(1, aliceSigner)
	channelAlice, err := lnwallet.NewLightningChannel(
		aliceSigner, aliceChannelState, alicePool, chainParams,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	_ = alicePool.Start()

	bobPool := lnwallet.NewSigPool(1, bobSigner)
	channelBob, err := lnwallet.NewLightningChannel(
		bobSigner, bobChannelState, bobPool, chainParams,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	_ = bobPool.Start()

	chainIO := &mock.ChainIO{
		BestHeight: broadcastHeight,
	}
	wallet := &lnwallet.LightningWallet{
		WalletController: &mock.WalletController{
			RootKey:               aliceKeyPriv,
			PublishedTransactions: publTx,
		},
	}

	// If mockSwitch is not set by the caller, set it to the default as the
	// caller does not need to control it.
	if mockSwitch == nil {
		mockSwitch = &mockMessageSwitch{}
	}

	nodeSignerAlice := netann.NewNodeSigner(aliceKeySigner)

	const chanActiveTimeout = time.Minute

	chanStatusMgr, err := netann.NewChanStatusManager(&netann.ChanStatusConfig{
		ChanStatusSampleInterval: 30 * time.Second,
		ChanEnableTimeout:        chanActiveTimeout,
		ChanDisableTimeout:       2 * time.Minute,
		DB:                       dbAlice.ChannelStateDB(),
		Graph:                    dbAlice.ChannelGraph(),
		MessageSigner:            nodeSignerAlice,
		OurPubKey:                aliceKeyPub,
		IsChannelActive:          func(lnwire.ChannelID) bool { return true },
		ApplyChannelUpdate:       func(*lnwire.ChannelUpdate) error { return nil },
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if err = chanStatusMgr.Start(); err != nil {
		return nil, nil, nil, err
	}

	errBuffer, err := queue.NewCircularBuffer(ErrorBufferSize)
	if err != nil {
		return nil, nil, nil, err
	}

	var pubKey [33]byte
	copy(pubKey[:], aliceKeyPub.SerializeCompressed())

	cfgAddr := &lnwire.NetAddress{
		IdentityKey: aliceKeyPub,
		Address:     aliceAddr,
		ChainNet:    wire.SimNet,
	}

	cfg := &Config{
		Addr:        cfgAddr,
		PubKeyBytes: pubKey,
		ErrorBuffer: errBuffer,
		ChainIO:     chainIO,
		Switch:      mockSwitch,

		ChanActiveTimeout: chanActiveTimeout,
		InterceptSwitch:   htlcswitch.NewInterceptableSwitch(nil),

		ChannelDB:      dbAlice.ChannelStateDB(),
		FeeEstimator:   estimator,
		Wallet:         wallet,
		ChainNotifier:  notifier,
		ChanStatusMgr:  chanStatusMgr,
		DisconnectPeer: func(b *secp256k1.PublicKey) error { return nil },
		ChainParams:    chainParams,
	}

	alicePeer := NewBrontide(*cfg)

	chanID := lnwire.NewChanIDFromOutPoint(channelAlice.ChannelPoint())
	alicePeer.activeChannels[chanID] = channelAlice

	alicePeer.wg.Add(1)
	go alicePeer.channelManager()

	return alicePeer, channelBob, cleanUpFunc, nil
}

// mockMessageSwitch is a mock implementation of the messageSwitch interface
// used for testing without relying on a *htlcswitch.Switch in unit tests.
type mockMessageSwitch struct {
	links []htlcswitch.ChannelUpdateHandler
}

// BestHeight currently returns a dummy value.
func (m *mockMessageSwitch) BestHeight() uint32 {
	return 0
}

// CircuitModifier currently returns a dummy value.
func (m *mockMessageSwitch) CircuitModifier() htlcswitch.CircuitModifier {
	return nil
}

// RemoveLink currently does nothing.
func (m *mockMessageSwitch) RemoveLink(cid lnwire.ChannelID) {}

// CreateAndAddLink currently returns a dummy value.
func (m *mockMessageSwitch) CreateAndAddLink(cfg htlcswitch.ChannelLinkConfig,
	lnChan *lnwallet.LightningChannel) error {

	return nil
}

// GetLinksByInterface returns the active links.
func (m *mockMessageSwitch) GetLinksByInterface(pub [33]byte) (
	[]htlcswitch.ChannelUpdateHandler, error) {

	return m.links, nil
}

// mockUpdateHandler is a mock implementation of the ChannelUpdateHandler
// interface. It is used in mockMessageSwitch's GetLinksByInterface method.
type mockUpdateHandler struct {
	cid lnwire.ChannelID
}

// newMockUpdateHandler creates a new mockUpdateHandler.
func newMockUpdateHandler(cid lnwire.ChannelID) *mockUpdateHandler {
	return &mockUpdateHandler{
		cid: cid,
	}
}

// HandleChannelUpdate currently does nothing.
func (m *mockUpdateHandler) HandleChannelUpdate(msg lnwire.Message) {}

// ChanID returns the mockUpdateHandler's cid.
func (m *mockUpdateHandler) ChanID() lnwire.ChannelID { return m.cid }

// Bandwidth currently returns a dummy value.
func (m *mockUpdateHandler) Bandwidth() lnwire.MilliAtom { return 0 }

// EligibleToForward currently returns a dummy value.
func (m *mockUpdateHandler) EligibleToForward() bool { return false }

// MayAddOutgoingHtlc currently returns nil.
func (m *mockUpdateHandler) MayAddOutgoingHtlc() error { return nil }

// ShutdownIfChannelClean currently returns nil.
func (m *mockUpdateHandler) ShutdownIfChannelClean() error { return nil }

type mockMessageConn struct {
	t *testing.T

	// MessageConn embeds our interface so that the mock does not need to
	// implement every function. The mock will panic if an unspecified function
	// is called.
	MessageConn

	// writtenMessages is a channel that our mock pushes written messages into.
	writtenMessages chan []byte
}

func newMockConn(t *testing.T, expectedMessages int) *mockMessageConn {
	return &mockMessageConn{
		t:               t,
		writtenMessages: make(chan []byte, expectedMessages),
	}
}

// SetWriteDeadline mocks setting write deadline for our conn.
func (m *mockMessageConn) SetWriteDeadline(time.Time) error {
	return nil
}

// Flush mocks a message conn flush.
func (m *mockMessageConn) Flush() (int, error) {
	return 0, nil
}

// WriteMessage mocks sending of a message on our connection. It will push
// the bytes sent into the mock's writtenMessages channel.
func (m *mockMessageConn) WriteMessage(msg []byte) error {
	select {
	case m.writtenMessages <- msg:
	case <-time.After(timeout):
		m.t.Fatalf("timeout sending message: %v", msg)
	}

	return nil
}

// assertWrite asserts that our mock as had WriteMessage called with the byte
// slice we expect.
func (m *mockMessageConn) assertWrite(expected []byte) {
	select {
	case actual := <-m.writtenMessages:
		require.Equal(m.t, expected, actual)

	case <-time.After(timeout):
		m.t.Fatalf("timeout waiting for write: %v", expected)
	}
}
