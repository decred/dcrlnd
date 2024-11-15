package walletunlocker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrlnd/aezeed"
	"github.com/decred/dcrlnd/chanbackup"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/macaroons"

	"decred.org/dcrwallet/v4/wallet"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
	"google.golang.org/grpc"
)

var (
	// ErrUnlockTimeout signals that we did not get the expected unlock
	// message before the timeout occurred.
	ErrUnlockTimeout = errors.New("got no unlock message before timeout")
)

// WalletUnlockParams holds the variables used to parameterize the unlocking of
// lnd's wallet after it has already been created.
type WalletUnlockParams struct {
	// Password is the public and private wallet passphrase.
	Password []byte

	// Birthday specifies the approximate time that this wallet was created.
	// This is used to bound any rescans on startup.
	Birthday time.Time

	// RecoveryWindow specifies the address lookahead when entering recovery
	// mode. A recovery will be attempted if this value is non-zero.
	RecoveryWindow uint32

	// Wallet is the loaded and unlocked Wallet. This is returned
	// from the unlocker service to avoid it being unlocked twice (once in
	// the unlocker service to check if the password is correct and again
	// later when lnd actually uses it). Because unlocking involves scrypt
	// which is resource intensive, we want to avoid doing it twice.
	Wallet *wallet.Wallet

	// Loader is the wallet loader used to create or open the corresponding
	// wallet.
	Loader *walletloader.Loader

	// Conn is the connection to the remote wallet when that is used
	// instead of an embedded dcrwallet instance.
	Conn *grpc.ClientConn

	// ChansToRestore a set of static channel backups that should be
	// restored before the main server instance starts up.
	ChansToRestore ChannelsToRecover

	// UnloadWallet is a function for unloading the wallet, which should
	// be called on shutdown.
	UnloadWallet func() error

	// StatelessInit signals that the user requested the daemon to be
	// initialized stateless, which means no unencrypted macaroons should be
	// written to disk.
	StatelessInit bool

	// MacResponseChan is the channel for sending back the admin macaroon to
	// the WalletUnlocker service.
	MacResponseChan chan []byte
}

// ChannelsToRecover wraps any set of packed (serialized+encrypted) channel
// back ups together. These can be passed in when unlocking the wallet, or
// creating a new wallet for the first time with an existing seed.
type ChannelsToRecover struct {
	// PackedMultiChanBackup is an encrypted and serialized multi-channel
	// backup.
	PackedMultiChanBackup chanbackup.PackedMulti

	// PackedSingleChanBackups is a series of encrypted and serialized
	// single-channel backup for one or more channels.
	PackedSingleChanBackups chanbackup.PackedSingles
}

// WalletInitMsg is a message sent by the UnlockerService when a user wishes to
// set up the internal wallet for the first time. The user MUST provide a
// passphrase, but is also able to provide their own source of entropy. If
// provided, then this source of entropy will be used to generate the wallet's
// HD seed. Otherwise, the wallet will generate one itself.
type WalletInitMsg struct {
	// Passphrase is the passphrase that will be used to encrypt the wallet
	// itself. This MUST be at least 8 characters.
	Passphrase []byte

	// WalletSeed is the deciphered cipher seed that the wallet should use
	// to initialize itself. The seed might be nil if the wallet should be
	// created from an extended master root key instead.
	WalletSeed *aezeed.CipherSeed

	// WalletExtendedKey is the wallet's extended master root key that
	// should be used instead of the seed, if non-nil. The extended key is
	// mutually exclusive to the wallet seed, but one of both is always set.
	WalletExtendedKey *hdkeychain.ExtendedKey

	// ExtendedKeyBirthday is the birthday of a wallet that's being restored
	// through an extended key instead of an aezeed.
	ExtendedKeyBirthday time.Time

	// WatchOnlyBirthday is the birthday of the master root key the above
	// watch-only account xpubs were derived from.
	WatchOnlyBirthday time.Time

	// WatchOnlyMasterFingerprint is the fingerprint of the master root key
	// the above watch-only account xpubs were derived from.
	WatchOnlyMasterFingerprint uint32

	// RecoveryWindow is the address look-ahead used when restoring a seed
	// with existing funds. A recovery window zero indicates that no
	// recovery should be attempted, such as after the wallet's initial
	// creation.
	RecoveryWindow uint32

	// ChanBackups a set of static channel backups that should be received
	// after the wallet has been initialized.
	ChanBackups ChannelsToRecover

	// StatelessInit signals that the user requested the daemon to be
	// initialized stateless, which means no unencrypted macaroons should be
	// written to disk.
	StatelessInit bool
}

// WalletUnlockMsg is a message sent by the UnlockerService when a user wishes
// to unlock the internal wallet after initial setup. The user can optionally
// specify a recovery window, which will resume an interrupted rescan for used
// addresses.
type WalletUnlockMsg struct {
	// Passphrase is the passphrase that will be used to encrypt the wallet
	// itself. This MUST be at least 8 characters.
	Passphrase []byte

	// RecoveryWindow is the address look-ahead used when restoring a seed
	// with existing funds. A recovery window zero indicates that no
	// recovery should be attempted, such as after the wallet's initial
	// creation, but before any addresses have been created.
	RecoveryWindow uint32

	// Wallet is the loaded and unlocked Wallet. This is returned through
	// the channel to avoid it being unlocked twice (once to check if the
	// password is correct, here in the WalletUnlocker and again later when
	// lnd actually uses it). Because unlocking involves scrypt which is
	// resource intensive, we want to avoid doing it twice.
	Wallet *wallet.Wallet

	Loader *walletloader.Loader

	// Conn is the connection to a remote wallet when the daemon has been
	// configured to connect to a wallet instead of using the embedded one.
	Conn *grpc.ClientConn

	// ChanBackups a set of static channel backups that should be received
	// after the wallet has been unlocked.
	ChanBackups ChannelsToRecover

	// UnloadWallet is a function for unloading the wallet, which should
	// be called on shutdown.
	UnloadWallet func() error

	// StatelessInit signals that the user requested the daemon to be
	// initialized stateless, which means no unencrypted macaroons should be
	// written to disk.
	StatelessInit bool
}

// UnlockerService implements the WalletUnlocker service used to provide lnd
// with a password for wallet encryption at startup. Additionally, during
// initial setup, users can provide their own source of entropy which will be
// used to generate the seed that's ultimately used within the wallet.
type UnlockerService struct {
	// InitMsgs is a channel that carries all wallet init messages.
	InitMsgs chan *WalletInitMsg

	// UnlockMsgs is a channel where unlock parameters provided by the rpc
	// client to be used to unlock and decrypt an existing wallet will be
	// sent.
	UnlockMsgs chan *WalletUnlockMsg

	// MacResponseChan is the channel for sending back the admin macaroon to
	// the WalletUnlocker service.
	MacResponseChan chan []byte

	chainDir  string
	netParams *chaincfg.Params

	// db is the db used for checking remote wallet unlocks.
	db atomic.Pointer[channeldb.DB]

	dcrwHost       string
	dcrwCert       string
	dcrwClientKey  string
	dcrwClientCert string
	dcrwAccount    int32

	// macaroonFiles is the path to the three generated macaroons with
	// different access permissions. These might not exist in a stateless
	// initialization of lnd.
	macaroonFiles []string

	// resetWalletTransactions indicates that the wallet state should be
	// reset on unlock to force a full chain rescan.
	resetWalletTransactions bool

	// LoaderOpts holds the functional options for the wallet loader.
	loaderOpts []walletloader.LoaderOption

	// macaroonDB is an instance of a database backend that stores all
	// macaroon root keys. This will be nil on initialization and must be
	// set using the SetMacaroonDB method as soon as it's available.
	macaroonDB kvdb.Backend
}

// New creates and returns a new UnlockerService.
func New(chainDir string, params *chaincfg.Params,
	macaroonFiles []string, dbTimeout time.Duration,
	dcrwHost, dcrwCert, dcrwClientKey,
	dcrwClientCert string, dcrwAccount int32) *UnlockerService {

	return &UnlockerService{
		InitMsgs:       make(chan *WalletInitMsg, 1),
		UnlockMsgs:     make(chan *WalletUnlockMsg, 1),
		dcrwHost:       dcrwHost,
		dcrwCert:       dcrwCert,
		dcrwClientKey:  dcrwClientKey,
		dcrwClientCert: dcrwClientCert,
		dcrwAccount:    dcrwAccount,

		// Make sure we buffer the channel is buffered so the main lnd
		// goroutine isn't blocking on writing to it.
		MacResponseChan:         make(chan []byte, 1),
		chainDir:                chainDir,
		netParams:               params,
		macaroonFiles:           macaroonFiles,
		resetWalletTransactions: false,
	}
}

// SetDB sets the DB used for checking that unlocking remote wallets works. This
// must be called before the service is
func (u *UnlockerService) SetDB(db *channeldb.DB) {
	u.db.Store(db)
}

// SetLoaderOpts can be used to inject wallet loader options after the unlocker
// service has been hooked to the main RPC server.
func (u *UnlockerService) SetLoaderOpts(loaderOpts []walletloader.LoaderOption) {
	u.loaderOpts = loaderOpts
}

// SetMacaroonDB can be used to inject the macaroon database after the unlocker
// service has been hooked to the main RPC server.
func (u *UnlockerService) SetMacaroonDB(macaroonDB kvdb.Backend) {
	u.macaroonDB = macaroonDB
}

func (u *UnlockerService) newLoader(recoveryWindow uint32) (*walletloader.Loader,
	error) {

	netDir := dcrwallet.NetworkDir(u.chainDir, u.netParams)
	return walletloader.NewLoader(
		u.netParams, netDir, recoveryWindow,
	), nil
}

// WalletExists returns whether a wallet exists on the file path the
// UnlockerService is using.
func (u *UnlockerService) WalletExists() (bool, error) {
	loader, err := u.newLoader(wallet.DefaultGapLimit)
	if err != nil {
		return false, err
	}
	return loader.WalletExists()
}

// GenSeed is the first method that should be used to instantiate a new lnd
// instance. This method allows a caller to generate a new aezeed cipher seed
// given an optional passphrase. If provided, the passphrase will be necessary
// to decrypt the cipherseed to expose the internal wallet seed.
//
// Once the cipherseed is obtained and verified by the user, the InitWallet
// method should be used to commit the newly generated seed, and create the
// wallet.
func (u *UnlockerService) GenSeed(_ context.Context,
	in *lnrpc.GenSeedRequest) (*lnrpc.GenSeedResponse, error) {

	// Before we start, we'll ensure that the wallet hasn't already created
	// so we don't show a *new* seed to the user if one already exists.
	loader, err := u.newLoader(wallet.DefaultGapLimit)
	if err != nil {
		return nil, err
	}

	walletExists, err := loader.WalletExists()
	if err != nil {
		return nil, err
	}
	if walletExists {
		return nil, fmt.Errorf("wallet already exists")
	}

	var entropy [aezeed.EntropySize]byte

	switch {
	// If the user provided any entropy, then we'll make sure it's sized
	// properly.
	case len(in.SeedEntropy) != 0 && len(in.SeedEntropy) != aezeed.EntropySize:
		return nil, fmt.Errorf("incorrect entropy length: expected "+
			"16 bytes, instead got %v bytes", len(in.SeedEntropy))

	// If the user provided the correct number of bytes, then we'll copy it
	// over into our buffer for usage.
	case len(in.SeedEntropy) == aezeed.EntropySize:
		copy(entropy[:], in.SeedEntropy)

	// Otherwise, we'll generate a fresh new set of bytes to use as entropy
	// to generate the seed.
	default:
		if _, err := rand.Read(entropy[:]); err != nil {
			return nil, err
		}
	}

	// Subtract 24h from the birthday, to ensure no reorgs could possibly
	// make us miss some transactions.
	birthday := time.Now().Add(-time.Hour * 24)

	// Now that we have our set of entropy, we'll create a new cipher seed
	// instance.
	//
	cipherSeed, err := aezeed.New(
		keychain.KeyDerivationVersion, &entropy, birthday,
	)
	if err != nil {
		return nil, err
	}

	// With our raw cipher seed obtained, we'll convert it into an encoded
	// mnemonic using the user specified pass phrase.
	mnemonic, err := cipherSeed.ToMnemonic(in.AezeedPassphrase)
	if err != nil {
		return nil, err
	}

	// Additionally, we'll also obtain the raw enciphered cipher seed as
	// well to return to the user.
	encipheredSeed, err := cipherSeed.Encipher(in.AezeedPassphrase)
	if err != nil {
		return nil, err
	}

	return &lnrpc.GenSeedResponse{
		CipherSeedMnemonic: mnemonic[:],
		EncipheredSeed:     encipheredSeed[:],
	}, nil
}

// extractChanBackups is a helper function that extracts the set of channel
// backups from the proto into a format that we'll pass to higher level
// sub-systems.
func extractChanBackups(chanBackups *lnrpc.ChanBackupSnapshot) *ChannelsToRecover {
	// If there aren't any populated channel backups, then we can exit
	// early as there's nothing to extract.
	if chanBackups == nil || (chanBackups.SingleChanBackups == nil &&
		chanBackups.MultiChanBackup == nil) {
		return nil
	}

	// Now that we know there's at least a single back up populated, we'll
	// extract the multi-chan backup (if it's there).
	var backups ChannelsToRecover
	if chanBackups.MultiChanBackup != nil {
		multiBackup := chanBackups.MultiChanBackup
		backups.PackedMultiChanBackup = multiBackup.MultiChanBackup
	}

	if chanBackups.SingleChanBackups == nil {
		return &backups
	}

	// Finally, we can extract all the single chan backups as well.
	for _, backup := range chanBackups.SingleChanBackups.ChanBackups {
		singleChanBackup := backup.ChanBackup

		backups.PackedSingleChanBackups = append(
			backups.PackedSingleChanBackups, singleChanBackup,
		)
	}

	return &backups
}

// InitWallet is used when lnd is starting up for the first time to fully
// initialize the daemon and its internal wallet. At the very least a wallet
// password must be provided. This will be used to encrypt sensitive material
// on disk.
//
// In the case of a recovery scenario, the user can also specify their aezeed
// mnemonic and passphrase. If set, then the daemon will use this prior state
// to initialize its internal wallet.
//
// Alternatively, this can be used along with the GenSeed RPC to obtain a
// seed, then present it to the user. Once it has been verified by the user,
// the seed can be fed into this RPC in order to commit the new wallet.
func (u *UnlockerService) InitWallet(ctx context.Context,
	in *lnrpc.InitWalletRequest) (*lnrpc.InitWalletResponse, error) {

	// Make sure the password meets our constraints.
	password := in.WalletPassword
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}

	// Require that the recovery window be non-negative.
	recoveryWindow := in.RecoveryWindow
	if recoveryWindow < 0 {
		return nil, fmt.Errorf("recovery window %d must be "+
			"non-negative", recoveryWindow)
	}
	if recoveryWindow == 0 {
		recoveryWindow = int32(wallet.DefaultGapLimit)
	}

	// We'll then open up the directory that will be used to store the
	// wallet's files so we can check if the wallet already exists.
	loader, err := u.newLoader(uint32(recoveryWindow))
	if err != nil {
		return nil, err
	}

	walletExists, err := loader.WalletExists()
	if err != nil {
		return nil, err
	}

	// If the wallet already exists, then we'll exit early as we can't
	// create the wallet if it already exists!
	if walletExists {
		return nil, fmt.Errorf("wallet already exists")
	}

	// At this point, we know the wallet doesn't already exist so we can
	// prepare the message that we'll send over the channel later.
	initMsg := &WalletInitMsg{
		Passphrase:     password,
		RecoveryWindow: uint32(recoveryWindow),
		StatelessInit:  in.StatelessInit,
	}

	// There are two supported ways to initialize the wallet. Either from
	// the aezeed or the final extended master key directly.
	switch {
	// Don't allow the user to specify both as that would be ambiguous.
	case len(in.CipherSeedMnemonic) > 0 && len(in.ExtendedMasterKey) > 0:
		return nil, fmt.Errorf("cannot specify both the cipher " +
			"seed mnemonic and the extended master key")

	// The aezeed is the preferred and default way of initializing a wallet.
	case len(in.CipherSeedMnemonic) > 0:
		// We'll map the user provided aezeed and passphrase into a
		// decoded cipher seed instance.
		var mnemonic aezeed.Mnemonic
		copy(mnemonic[:], in.CipherSeedMnemonic)

		// If we're unable to map it back into the ciphertext, then
		// either the mnemonic is wrong, or the passphrase is wrong.
		cipherSeed, err := mnemonic.ToCipherSeed(in.AezeedPassphrase)
		if err != nil {
			return nil, err
		}

		initMsg.WalletSeed = cipherSeed

	// To support restoring a wallet where the seed isn't known or a wallet
	// created externally to lnd, we also allow the extended master key
	// (xprv) to be imported directly. This is what'll be stored in the
	// dcrwallet database anyway.
	case len(in.ExtendedMasterKey) > 0:
		extendedKey, err := hdkeychain.NewKeyFromString(
			in.ExtendedMasterKey, u.netParams,
		)
		if err != nil {
			return nil, err
		}

		// The on-chain wallet of lnd is going to derive keys based on
		// the BIP49/84 key derivation paths from this root key. To make
		// sure we use default derivation paths, we want to avoid
		// deriving keys from something other than the master key (at
		// depth 0, denoted with "m/" in BIP32 notation).
		if extendedKey.Depth() != 0 {
			return nil, fmt.Errorf("extended master key must " +
				"be at depth 0 not a child key")
		}

		// Because we need the master key (at depth 0), it must be an
		// extended private key as the first levels of BIP49/84
		// derivation paths are hardened, which isn't possible with
		// extended public keys.
		if !extendedKey.IsPrivate() {
			return nil, fmt.Errorf("extended master key must " +
				"contain private keys")
		}

		// When importing a wallet from its extended private key we
		// don't know the birthday as that information is not encoded in
		// that format. We therefore must set an arbitrary date to start
		// rescanning at if the user doesn't provide an explicit value
		// for it. Since lnd only uses SegWit addresses, we pick the
		// date of the first block that contained SegWit transactions
		// (481824).
		initMsg.ExtendedKeyBirthday = time.Date(
			2017, time.August, 24, 1, 57, 37, 0, time.UTC,
		)
		if in.ExtendedMasterKeyBirthdayTimestamp != 0 {
			initMsg.ExtendedKeyBirthday = time.Unix(
				int64(in.ExtendedMasterKeyBirthdayTimestamp), 0,
			)
		}

		initMsg.WalletExtendedKey = extendedKey

	// The third option for creating a wallet is the watch-only mode:
	// Instead of providing the master root key directly, each individual
	// account is passed as an extended public key only. Because of the
	// hardened derivation path up to the account (depth 3), it is not
	// possible to create a master root extended _public_ key. Therefore, an
	// xpub must be derived and passed into the unlocker for _every_ account
	// lnd expects.
	case in.WatchOnly != nil && len(in.WatchOnly.Accounts) > 0:
		return nil, fmt.Errorf("watch-only wallets are not supported in dcrlnd")

	// No key material was set, no wallet can be created.
	default:
		return nil, fmt.Errorf("must either specify cipher seed " +
			"mnemonic or the extended master key")
	}

	// Before we return the unlock payload, we'll check if we can extract
	// any channel backups to pass up to the higher level sub-system.
	chansToRestore := extractChanBackups(in.ChannelBackups)
	if chansToRestore != nil {
		initMsg.ChanBackups = *chansToRestore
	}

	// Deliver the initialization message back to the main daemon.
	select {
	case u.InitMsgs <- initMsg:
		// We need to read from the channel to let the daemon continue
		// its work and to get the admin macaroon. Once the response
		// arrives, we directly forward it to the client.
		select {
		case adminMac := <-u.MacResponseChan:
			return &lnrpc.InitWalletResponse{
				AdminMacaroon: adminMac,
			}, nil

		case <-ctx.Done():
			return nil, ErrUnlockTimeout
		}

	case <-ctx.Done():
		return nil, ErrUnlockTimeout
	}
}

// LoadAndUnlock creates a loader for the wallet and tries to unlock the wallet
// with the given password and recovery window. If the drop wallet transactions
// flag is set, the history state drop is performed before unlocking the wallet
// yet again.
func (u *UnlockerService) LoadAndUnlock(ctx context.Context, password []byte,
	recoveryWindow uint32) (*wallet.Wallet, func() error, error) {

	loader, err := u.newLoader(recoveryWindow)
	if err != nil {
		return nil, nil, err
	}

	// Check if wallet already exists.
	walletExists, err := loader.WalletExists()
	if err != nil {
		return nil, nil, err
	}

	if !walletExists {
		// Cannot unlock a wallet that does not exist!
		return nil, nil, fmt.Errorf("wallet not found")
	}

	// Try opening the existing wallet with the provided password.
	unlockedWallet, err := loader.OpenExistingWallet(ctx, password)
	if err != nil {
		// Could not open wallet, most likely this means that provided
		// password was incorrect.
		return nil, nil, err
	}

	// The user requested to drop their whole wallet transaction state to
	// force a full chain rescan for wallet addresses. Dropping the state
	// only properly takes effect after opening the wallet. That's why we
	// start, drop, stop and start again.
	if u.resetWalletTransactions {
		return nil, nil, fmt.Errorf("dropping wallet txs is not supported in dcrlnd")
	}

	return unlockedWallet, loader.UnloadWallet, nil
}

// UnlockWallet sends the password provided by the incoming UnlockWalletRequest
// over the UnlockMsgs channel in case it successfully decrypts an existing
// wallet found in the chain's wallet database directory.
func (u *UnlockerService) UnlockWallet(ctx context.Context,
	in *lnrpc.UnlockWalletRequest) (*lnrpc.UnlockWalletResponse, error) {

	if u.dcrwHost != "" && u.dcrwCert != "" {
		// Using a remote wallet.
		return u.unlockRemoteWallet(ctx, in)
	}

	password := in.WalletPassword
	recoveryWindow := uint32(in.RecoveryWindow)
	if recoveryWindow < wallet.DefaultGapLimit {
		recoveryWindow = wallet.DefaultGapLimit
	}

	unlockedWallet, unloadFn, err := u.LoadAndUnlock(
		ctx, password, recoveryWindow,
	)
	if err != nil {
		return nil, err
	}

	// We successfully opened the wallet and pass the instance back to
	// avoid it needing to be unlocked again.
	walletUnlockMsg := &WalletUnlockMsg{
		Passphrase:     password,
		RecoveryWindow: recoveryWindow,
		Wallet:         unlockedWallet,
		UnloadWallet:   unloadFn,
		StatelessInit:  in.StatelessInit,
	}

	// Before we return the unlock payload, we'll check if we can extract
	// any channel backups to pass up to the higher level sub-system.
	chansToRestore := extractChanBackups(in.ChannelBackups)
	if chansToRestore != nil {
		walletUnlockMsg.ChanBackups = *chansToRestore
	}

	// At this point we were able to open the existing wallet with the
	// provided password. We send the password over the UnlockMsgs
	// channel, such that it can be used by lnd to open the wallet.
	select {
	case u.UnlockMsgs <- walletUnlockMsg:
		// We need to read from the channel to let the daemon continue
		// its work. But we don't need the returned macaroon for this
		// operation, so we read it but then discard it.
		select {
		case <-u.MacResponseChan:
			return &lnrpc.UnlockWalletResponse{}, nil

		case <-ctx.Done():
			return nil, ErrUnlockTimeout
		}

	case <-ctx.Done():
		return nil, ErrUnlockTimeout
	}
}

// ChangePassword changes the password of the wallet and sends the new password
// across the UnlockPasswords channel to automatically unlock the wallet if
// successful.
func (u *UnlockerService) ChangePassword(ctx context.Context,
	in *lnrpc.ChangePasswordRequest) (*lnrpc.ChangePasswordResponse, error) {

	loader, err := u.newLoader(wallet.DefaultGapLimit)
	if err != nil {
		return nil, err
	}

	// First, we'll make sure the wallet exists for the specific chain and
	// network.
	walletExists, err := loader.WalletExists()
	if err != nil {
		return nil, err
	}

	if !walletExists {
		return nil, errors.New("wallet not found")
	}

	publicPw := in.CurrentPassword
	privatePw := in.CurrentPassword

	// If the current password is blank, we'll assume the user is coming
	// from a --noseedbackup state, so we'll use the default passwords.
	if len(in.CurrentPassword) == 0 {
		publicPw = lnwallet.DefaultPublicPassphrase
		privatePw = lnwallet.DefaultPrivatePassphrase
	}

	// Make sure the new password meets our constraints.
	if err := ValidatePassword(in.NewPassword); err != nil {
		return nil, err
	}

	// Load the existing wallet in order to proceed with the password change.
	w, err := loader.OpenExistingWallet(ctx, publicPw)
	if err != nil {
		return nil, err
	}

	// Now that we've opened the wallet, we need to close it in case of an
	// error. But not if we succeed, then the caller must close it.
	orderlyReturn := false
	defer func() {
		if !orderlyReturn {
			_ = loader.UnloadWallet()
		}
	}()

	// Before we actually change the password, we need to check if all flags
	// were set correctly. The content of the previously generated macaroon
	// files will become invalid after we generate a new root key. So we try
	// to delete them here and they will be recreated during normal startup
	// later. If they are missing, this is only an error if the
	// stateless_init flag was not set.
	if in.NewMacaroonRootKey || in.StatelessInit {
		for _, file := range u.macaroonFiles {
			err := os.Remove(file)
			if err != nil && !in.StatelessInit {
				return nil, fmt.Errorf("could not remove "+
					"macaroon file: %v. if the wallet "+
					"was initialized stateless please "+
					"add the --stateless_init "+
					"flag", err)
			}
		}
	}

	// Attempt to change both the public and private passphrases for the
	// wallet. This will be done atomically in order to prevent one
	// passphrase change from being successful and not the other.
	//
	// TODO(decred) This is not an atomic operation. Discuss whether we want to
	// actually use the public pssword.
	err = w.ChangePrivatePassphrase(ctx, privatePw, in.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("unable to change wallet private passphrase: "+
			"%v", err)
	}
	err = w.ChangePublicPassphrase(ctx, publicPw, in.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("unable to change wallet public passphrase: "+
			"%v", err)
	}

	// The next step is to load the macaroon database, change the password
	// then close it again.
	// Attempt to open the macaroon DB, unlock it and then change
	// the passphrase.
	macaroonService, err := macaroons.NewService(
		u.macaroonDB, "lnd", in.StatelessInit,
	)
	if err != nil {
		return nil, err
	}

	err = macaroonService.CreateUnlock(&privatePw)
	if err != nil {
		closeErr := macaroonService.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("could not create unlock: %v "+
				"--> follow-up error when closing: %v", err,
				closeErr)
		}
		return nil, err
	}
	err = macaroonService.ChangePassword(privatePw, in.NewPassword)
	if err != nil {
		closeErr := macaroonService.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("could not change password: %v "+
				"--> follow-up error when closing: %v", err,
				closeErr)
		}
		return nil, err
	}

	// If requested by the user, attempt to replace the existing
	// macaroon root key with a new one.
	if in.NewMacaroonRootKey {
		err = macaroonService.GenerateNewRootKey()
		if err != nil {
			closeErr := macaroonService.Close()
			if closeErr != nil {
				return nil, fmt.Errorf("could not generate "+
					"new root key: %v --> follow-up error "+
					"when closing: %v", err, closeErr)
			}
			return nil, err
		}
	}

	err = macaroonService.Close()
	if err != nil {
		return nil, fmt.Errorf("could not close macaroon service: %v",
			err)
	}

	// Finally, send the new password across the UnlockPasswords channel to
	// automatically unlock the wallet.
	walletUnlockMsg := &WalletUnlockMsg{
		Passphrase:    in.NewPassword,
		Wallet:        w,
		StatelessInit: in.StatelessInit,
		UnloadWallet:  loader.UnloadWallet,
	}
	select {
	case u.UnlockMsgs <- walletUnlockMsg:
		// We need to read from the channel to let the daemon continue
		// its work and to get the admin macaroon. Once the response
		// arrives, we directly forward it to the client.
		orderlyReturn = true
		select {
		case adminMac := <-u.MacResponseChan:
			return &lnrpc.ChangePasswordResponse{
				AdminMacaroon: adminMac,
			}, nil

		case <-ctx.Done():
			return nil, ErrUnlockTimeout
		}

	case <-ctx.Done():
		return nil, ErrUnlockTimeout
	}
}

// ValidatePassword assures the password meets all of our constraints.
func ValidatePassword(password []byte) error {
	// Passwords should have a length of at least 8 characters.
	if len(password) < 8 {
		return errors.New("password must have at least 8 characters")
	}

	return nil
}
