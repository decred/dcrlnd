package dcrlnd

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"path/filepath"
	"time"

	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/btcwalletcompat"
	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/lncfg"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
	"github.com/decred/dcrlnd/macaroons"
	"github.com/decred/dcrlnd/rpcperms"
	"github.com/decred/dcrlnd/signal"
	"github.com/decred/dcrlnd/walletunlocker"
	"github.com/decred/dcrlnd/watchtower"
	"github.com/decred/dcrlnd/watchtower/wtclient"
	"github.com/decred/dcrlnd/watchtower/wtdb"
	"github.com/decred/slog"
	proxy "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"gopkg.in/macaroon-bakery.v2/bakery"
)

// GrpcRegistrar is an interface that must be satisfied by an external subserver
// that wants to be able to register its own gRPC server onto lnd's main
// grpc.Server instance.
type GrpcRegistrar interface {
	// RegisterGrpcSubserver is called for each net.Listener on which lnd
	// creates a grpc.Server instance. External subservers implementing this
	// method can then register their own gRPC server structs to the main
	// server instance.
	RegisterGrpcSubserver(*grpc.Server) error
}

// RestRegistrar is an interface that must be satisfied by an external subserver
// that wants to be able to register its own REST mux onto lnd's main
// proxy.ServeMux instance.
type RestRegistrar interface {
	// RegisterRestSubserver is called after lnd creates the main
	// proxy.ServeMux instance. External subservers implementing this method
	// can then register their own REST proxy stubs to the main server
	// instance.
	RegisterRestSubserver(context.Context, *proxy.ServeMux, string,
		[]grpc.DialOption) error
}

// ExternalValidator is an interface that must be satisfied by an external
// macaroon validator.
type ExternalValidator interface {
	macaroons.MacaroonValidator

	// Permissions returns the permissions that the external validator is
	// validating. It is a map between the full HTTP URI of each RPC and its
	// required macaroon permissions. If multiple action/entity tuples are
	// specified per URI, they are all required. See rpcserver.go for a list
	// of valid action and entity values.
	Permissions() map[string][]bakery.Op
}

// DatabaseBuilder is an interface that must be satisfied by the implementation
// that provides lnd's main database backend instances.
type DatabaseBuilder interface {
	// BuildDatabase extracts the current databases that we'll use for
	// normal operation in the daemon. A function closure that closes all
	// opened databases is also returned.
	BuildDatabase(ctx context.Context) (*DatabaseInstances, func(), error)
}

// WalletConfigBuilder is an interface that must be satisfied by a custom wallet
// implementation.
type WalletConfigBuilder interface {
	// BuildWalletConfig is responsible for creating or unlocking and then
	// fully initializing a wallet.
	BuildWalletConfig(context.Context, *DatabaseInstances,
		*rpcperms.InterceptorChain,
		[]*ListenerWithSignal) (*chainreg.PartialChainControl,
		*chainreg.WalletConfig, func(), error)
}

// ChainControlBuilder is an interface that must be satisfied by a custom wallet
// implementation.
type ChainControlBuilder interface {
	// BuildChainControl is responsible for creating a fully populated chain
	// control instance from a wallet.
	BuildChainControl(*chainreg.PartialChainControl,
		*chainreg.WalletConfig) (*chainreg.ChainControl, func(), error)
}

// ImplementationCfg is a struct that holds all configuration items for
// components that can be implemented outside lnd itself.
type ImplementationCfg struct {
	// GrpcRegistrar is a type that can register additional gRPC subservers
	// before the main gRPC server is started.
	GrpcRegistrar

	// RestRegistrar is a type that can register additional REST subservers
	// before the main REST proxy is started.
	RestRegistrar

	// ExternalValidator is a type that can provide external macaroon
	// validation.
	ExternalValidator

	// DatabaseBuilder is a type that can provide lnd's main database
	// backend instances.
	DatabaseBuilder

	// WalletConfigBuilder is a type that can provide a wallet configuration
	// with a fully loaded and unlocked wallet.
	WalletConfigBuilder

	// ChainControlBuilder is a type that can provide a custom wallet
	// implementation.
	ChainControlBuilder
}

// DefaultWalletImpl is the default implementation of our normal, btcwallet
// backed configuration.
type DefaultWalletImpl struct {
	cfg         *Config
	logger      slog.Logger
	interceptor signal.Interceptor

	pwService *walletunlocker.UnlockerService
}

// NewDefaultWalletImpl creates a new default wallet implementation.
func NewDefaultWalletImpl(cfg *Config, logger slog.Logger,
	interceptor signal.Interceptor) *DefaultWalletImpl {

	return &DefaultWalletImpl{
		cfg:         cfg,
		logger:      logger,
		interceptor: interceptor,
		pwService:   createWalletUnlockerService(cfg),
	}
}

// RegisterRestSubserver is called after lnd creates the main proxy.ServeMux
// instance. External subservers implementing this method can then register
// their own REST proxy stubs to the main server instance.
//
// NOTE: This is part of the GrpcRegistrar interface.
func (d *DefaultWalletImpl) RegisterRestSubserver(ctx context.Context,
	mux *proxy.ServeMux, restProxyDest string,
	restDialOpts []grpc.DialOption) error {

	return lnrpc.RegisterWalletUnlockerHandlerFromEndpoint(
		ctx, mux, restProxyDest, restDialOpts,
	)
}

// RegisterGrpcSubserver is called for each net.Listener on which lnd creates a
// grpc.Server instance. External subservers implementing this method can then
// register their own gRPC server structs to the main server instance.
//
// NOTE: This is part of the GrpcRegistrar interface.
func (d *DefaultWalletImpl) RegisterGrpcSubserver(s *grpc.Server) error {
	lnrpc.RegisterWalletUnlockerServer(s, d.pwService)

	return nil
}

// ValidateMacaroon extracts the macaroon from the context's gRPC metadata,
// checks its signature, makes sure all specified permissions for the called
// method are contained within and finally ensures all caveat conditions are
// met. A non-nil error is returned if any of the checks fail.
//
// NOTE: This is part of the ExternalValidator interface.
func (d *DefaultWalletImpl) ValidateMacaroon(ctx context.Context,
	requiredPermissions []bakery.Op, fullMethod string) error {

	// Because the default implementation does not return any permissions,
	// we shouldn't be registered as an external validator at all and this
	// should never be invoked.
	return fmt.Errorf("default implementation does not support external " +
		"macaroon validation")
}

// Permissions returns the permissions that the external validator is
// validating. It is a map between the full HTTP URI of each RPC and its
// required macaroon permissions. If multiple action/entity tuples are specified
// per URI, they are all required. See rpcserver.go for a list of valid action
// and entity values.
//
// NOTE: This is part of the ExternalValidator interface.
func (d *DefaultWalletImpl) Permissions() map[string][]bakery.Op {
	return nil
}

// BuildWalletConfig is responsible for creating or unlocking and then
// fully initializing a wallet.
//
// NOTE: This is part of the WalletConfigBuilder interface.
func (d *DefaultWalletImpl) BuildWalletConfig(ctx context.Context,
	dbs *DatabaseInstances, interceptorChain *rpcperms.InterceptorChain,
	grpcListeners []*ListenerWithSignal) (*chainreg.PartialChainControl,
	*chainreg.WalletConfig, func(), error) {

	// Keep track of our various cleanup functions. We use a defer function
	// as well to not repeat ourselves with every return statement.
	var (
		cleanUpTasks []func()
		earlyExit    = true
		cleanUp      = func() {
			for _, fn := range cleanUpTasks {
				if fn == nil {
					continue
				}

				fn()
			}
		}
	)
	defer func() {
		if earlyExit {
			cleanUp()
		}
	}()

	// Initialize a new block cache.
	blockCache := blockcache.NewBlockCache(d.cfg.BlockCacheSize)

	var (
		walletInitParams = walletunlocker.WalletUnlockParams{
			// In case we do auto-unlock, we need to be able to send
			// into the channel without blocking so we buffer it.
			MacResponseChan: make(chan []byte, 1),
		}
		privateWalletPw = lnwallet.DefaultPrivatePassphrase
		publicWalletPw  = lnwallet.DefaultPublicPassphrase
	)

	// If the user didn't request a seed, then we'll manually assume a
	// wallet birthday of now, as otherwise the seed would've specified
	// this information.
	walletInitParams.Birthday = time.Now()

	// Remote wallet init has some differences.
	isRemoteWallet := d.cfg.Dcrwallet.GRPCHost != "" && d.cfg.Dcrwallet.CertPath != ""

	d.pwService.SetDB(dbs.ChanStateDB)
	d.pwService.SetLoaderOpts([]walletloader.LoaderOption{dbs.WalletDB})
	d.pwService.SetMacaroonDB(dbs.MacaroonDB)
	walletExists, err := d.pwService.WalletExists()
	if err != nil {
		return nil, nil, nil, err
	}

	if !walletExists {
		interceptorChain.SetWalletNotCreated()
	} else {
		interceptorChain.SetWalletLocked()
	}

	// If we've started in auto unlock mode, then a wallet should already
	// exist because we don't want to enable the RPC unlocker in that case
	// for security reasons (an attacker could inject their seed since the
	// RPC is unauthenticated). Only if the user explicitly wants to allow
	// wallet creation we don't error out here.
	if d.cfg.WalletUnlockPasswordFile != "" && !walletExists &&
		!d.cfg.WalletUnlockAllowCreate && !isRemoteWallet {

		return nil, nil, nil, fmt.Errorf("wallet unlock password file " +
			"was specified but wallet does not exist; initialize " +
			"the wallet before using auto unlocking")
	}

	// What wallet mode are we running in? We've already made sure the no
	// seed backup and auto unlock aren't both set during config parsing.
	switch {
	// No seed backup means we're also using the default password.
	case d.cfg.NoSeedBackup && !isRemoteWallet:
		// We continue normally, the default password has already been
		// set above.
		d.logger.Warnf("RUNNING IN NO SEED BACKUP MODE")
		wallet, err := noSeedBackupWalletInit(ctx, d.cfg, privateWalletPw, publicWalletPw)
		if err != nil {
			return nil, nil, nil, err
		}
		walletInitParams.Wallet = wallet
		walletInitParams.Password = privateWalletPw

	// A password for unlocking is provided in a file.
	case d.cfg.WalletUnlockPasswordFile != "" && walletExists:
		d.logger.Infof("Attempting automatic wallet unlock with " +
			"password provided in file")
		pwBytes, err := ioutil.ReadFile(d.cfg.WalletUnlockPasswordFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading "+
				"password from file %s: %v",
				d.cfg.WalletUnlockPasswordFile, err)
		}

		// Remove any newlines at the end of the file. The lndinit tool
		// won't ever write a newline but maybe the file was provisioned
		// by another process or user.
		pwBytes = bytes.TrimRight(pwBytes, "\r\n")

		// We have the password now, we can ask the unlocker service to
		// do the unlock for us.
		unlockedWallet, unloadWalletFn, err := d.pwService.LoadAndUnlock(
			ctx, pwBytes, 0,
		)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error unlocking "+
				"wallet with password from file: %v", err)
		}

		cleanUpTasks = append(cleanUpTasks, func() {
			if err := unloadWalletFn(); err != nil {
				d.logger.Errorf("Could not unload wallet: %v",
					err)
			}
		})

		privateWalletPw = pwBytes
		publicWalletPw = pwBytes
		walletInitParams.Wallet = unlockedWallet
		walletInitParams.UnloadWallet = unloadWalletFn

	// If none of the automatic startup options are selected, we fall back
	// to the default behavior of waiting for the wallet creation/unlocking
	// over RPC.
	default:
		if err := d.interceptor.Notifier.NotifyReady(false); err != nil {
			return nil, nil, nil, err
		}

		params, err := waitForWalletPassword(
			d.cfg, d.pwService, []walletloader.LoaderOption{dbs.WalletDB},
			d.interceptor.ShutdownChannel(),
		)
		if err != nil {
			err := fmt.Errorf("unable to set up wallet password "+
				"listeners: %v", err)
			d.logger.Error(err)
			return nil, nil, nil, err
		}

		walletInitParams = *params
		privateWalletPw = walletInitParams.Password
		publicWalletPw = walletInitParams.Password
		cleanUpTasks = append(cleanUpTasks, func() {
			if err := walletInitParams.UnloadWallet(); err != nil {
				d.logger.Errorf("Could not unload wallet: %v",
					err)
			}
		})

		if walletInitParams.RecoveryWindow > 0 {
			d.logger.Infof("Wallet recovery mode enabled with "+
				"address lookahead of %d addresses",
				walletInitParams.RecoveryWindow)
		}
	}

	var macaroonService *macaroons.Service
	if !d.cfg.NoMacaroons {
		// Create the macaroon authentication/authorization service.
		macaroonService, err = macaroons.NewService(
			dbs.MacaroonDB, "lnd", walletInitParams.StatelessInit,
			macaroons.IPLockChecker,
			macaroons.CustomChecker(interceptorChain),
		)
		if err != nil {
			err := fmt.Errorf("unable to set up macaroon "+
				"authentication: %v", err)
			d.logger.Error(err)
			return nil, nil, nil, err
		}
		cleanUpTasks = append(cleanUpTasks, func() {
			if err := macaroonService.Close(); err != nil {
				d.logger.Errorf("Could not close macaroon "+
					"service: %v", err)
			}
		})

		// Try to unlock the macaroon store with the private password.
		// Ignore ErrAlreadyUnlocked since it could be unlocked by the
		// wallet unlocker.
		err = macaroonService.CreateUnlock(&privateWalletPw)
		if err != nil && err != macaroons.ErrAlreadyUnlocked {
			err := fmt.Errorf("unable to unlock macaroons: %v", err)
			d.logger.Error(err)
			return nil, nil, nil, err
		}

		// In case we actually needed to unlock the wallet, we now need
		// to create an instance of the admin macaroon and send it to
		// the unlocker so it can forward it to the user. In no seed
		// backup mode, there's nobody listening on the channel and we'd
		// block here forever.
		if !d.cfg.NoSeedBackup || isRemoteWallet {
			adminMacBytes, err := bakeMacaroon(
				ctx, macaroonService, adminPermissions(),
			)
			if err != nil {
				return nil, nil, nil, err
			}

			// The channel is buffered by one element so writing
			// should not block here.
			walletInitParams.MacResponseChan <- adminMacBytes

			for _, lis := range grpcListeners {
				if lis.MacChan != nil {
					lis.MacChan <- adminMacBytes
				}
			}
		}

		// If the user requested a stateless initialization, no macaroon
		// files should be created.
		if !walletInitParams.StatelessInit &&
			!fileExists(d.cfg.AdminMacPath) &&
			!fileExists(d.cfg.ReadMacPath) &&
			!fileExists(d.cfg.InvoiceMacPath) {

			// Create macaroon files for lncli to use if they don't
			// exist.
			err = genMacaroons(
				ctx, macaroonService, d.cfg.AdminMacPath,
				d.cfg.ReadMacPath, d.cfg.InvoiceMacPath,
			)
			if err != nil {
				err := fmt.Errorf("unable to create macaroons "+
					"%v", err)
				d.logger.Error(err)
				return nil, nil, nil, err
			}
		}

		// As a security service to the user, if they requested
		// stateless initialization and there are macaroon files on disk
		// we log a warning.
		if walletInitParams.StatelessInit {
			msg := "Found %s macaroon on disk (%s) even though " +
				"--stateless_init was requested. Unencrypted " +
				"state is accessible by the host system. You " +
				"should change the password and use " +
				"--new_mac_root_key with --stateless_init to " +
				"clean up and invalidate old macaroons."

			if fileExists(d.cfg.AdminMacPath) {
				d.logger.Warnf(msg, "admin", d.cfg.AdminMacPath)
			}
			if fileExists(d.cfg.ReadMacPath) {
				d.logger.Warnf(msg, "readonly", d.cfg.ReadMacPath)
			}
			if fileExists(d.cfg.InvoiceMacPath) {
				d.logger.Warnf(msg, "invoice", d.cfg.InvoiceMacPath)
			}
		}

		// We add the macaroon service to our RPC interceptor. This
		// will start checking macaroons against permissions on every
		// RPC invocation.
		interceptorChain.AddMacaroonService(macaroonService)
	}

	// Now that the wallet password has been provided, transition the RPC
	// state into Unlocked.
	interceptorChain.SetWalletUnlocked()

	// Since calls to the WalletUnlocker service wait for a response on the
	// macaroon channel, we close it here to make sure they return in case
	// we did not return the admin macaroon above. This will be the case if
	// --no-macaroons is used.
	close(walletInitParams.MacResponseChan)

	// We'll also close all the macaroon channels since lnd is done sending
	// macaroon data over it.
	for _, lis := range grpcListeners {
		if lis.MacChan != nil {
			close(lis.MacChan)
		}
	}

	// With the information parsed from the configuration, create valid
	// instances of the pertinent interfaces required to operate the
	// Lightning Network Daemon.
	//
	// When we create the chain control, we need storage for the height
	// hints and also the wallet itself, for these two we want them to be
	// replicated, so we'll pass in the remote channel DB instance.
	chainControlCfg := &chainreg.Config{
		Decred:                      d.cfg.Decred,
		PrimaryChain:                d.cfg.registeredChains.PrimaryChain,
		HeightHintCacheQueryDisable: d.cfg.HeightHintCacheQueryDisable,
		DcrdMode:                    d.cfg.DcrdMode,
		DcrwMode:                    d.cfg.Dcrwallet,
		FullDB:                      dbs.ChanStateDB,
		HeightHintDB:                dbs.HeightHintDB,
		ChanStateDB:                 dbs.ChanStateDB.ChannelStateDB(),
		ActiveNetParams:             d.cfg.ActiveNetParams,
		FeeURL:                      d.cfg.FeeURL,
		Dialer: func(addr string) (net.Conn, error) {
			return d.cfg.net.Dial(
				"tcp", addr, d.cfg.ConnectionTimeout,
			)
		},
		BlockCache:         blockCache,
		WalletUnlockParams: &walletInitParams,
	}

	// Let's go ahead and create the partial chain control now that is only
	// dependent on our configuration and doesn't require any wallet
	// specific information.
	partialChainControl, pccCleanup, err := chainreg.NewPartialChainControl(
		chainControlCfg,
	)
	cleanUpTasks = append(cleanUpTasks, pccCleanup)
	if err != nil {
		err := fmt.Errorf("unable to create partial chain control: %v",
			err)
		d.logger.Error(err)
		return nil, nil, nil, err
	}

	walletConfig := &chainreg.WalletConfig{
		PrivatePass:    privateWalletPw,
		PublicPass:     publicWalletPw,
		Birthday:       walletInitParams.Birthday,
		RecoveryWindow: walletInitParams.RecoveryWindow,
		AccountNb:      d.cfg.Dcrwallet.AccountNumber,
		/*
			NetParams:      d.cfg.ActiveNetParams.Params,
			CoinType:       d.cfg.ActiveNetParams.CoinType,
			Wallet:         walletInitParams.Wallet,
			LoaderOptions:  []btcwallet.LoaderOption{dbs.WalletDB},
			ChainSource:    partialChainControl.ChainSource,
		*/
	}

	// Parse coin selection strategy.
	switch d.cfg.CoinSelectionStrategy {
	case "largest":
		walletConfig.CoinSelectionStrategy = btcwalletcompat.CoinSelectionLargest

	case "random":
		walletConfig.CoinSelectionStrategy = btcwalletcompat.CoinSelectionRandom

	default:
		return nil, nil, nil, fmt.Errorf("unknown coin selection "+
			"strategy %v", d.cfg.CoinSelectionStrategy)
	}

	// Initialize the appropriate syncer.
	switch {
	case !isRemoteWallet && !d.cfg.Dcrwallet.SPV:
		walletConfig.Syncer, err = dcrwallet.NewRPCSyncer(*partialChainControl.RPCConfig,
			d.cfg.ActiveNetParams.Params)
	case !isRemoteWallet && d.cfg.Dcrwallet.SPV:
		spvCfg := &dcrwallet.SPVSyncerConfig{
			Peers:      d.cfg.Dcrwallet.SPVConnect,
			Net:        d.cfg.ActiveNetParams.Params,
			AppDataDir: filepath.Join(d.cfg.Decred.ChainDir),
			DialFunc:   d.cfg.Dcrwallet.DialFunc,
		}
		walletConfig.Syncer, err = dcrwallet.NewSPVSyncer(spvCfg)
	}

	if err != nil {
		return nil, nil, nil, err
	}

	earlyExit = false
	return partialChainControl, walletConfig, cleanUp, nil
}

// BuildChainControl is responsible for creating a fully populated chain
// control instance from a wallet.
//
// NOTE: This is part of the ChainControlBuilder interface.
func (d *DefaultWalletImpl) BuildChainControl(
	partialChainControl *chainreg.PartialChainControl,
	walletConfig *chainreg.WalletConfig) (*chainreg.ChainControl, func(), error) {

	cfg := partialChainControl.Cfg

	var chainIO lnwallet.BlockChainIO
	if partialChainControl.Cfg.Decred.Node == "dcrd" {
		var err error
		chainIO, err = dcrwallet.NewRPCChainIO(*partialChainControl.RPCConfig,
			cfg.ActiveNetParams.Params, cfg.BlockCache)
		if err != nil {
			return nil, nil, err
		}
	}

	dcrwConfig := &dcrwallet.Config{
		Syncer:         walletConfig.Syncer,
		ChainIO:        chainIO,
		PrivatePass:    walletConfig.PrivatePass,
		PublicPass:     walletConfig.PublicPass,
		Birthday:       walletConfig.Birthday,
		RecoveryWindow: walletConfig.RecoveryWindow,
		DataDir:        cfg.Decred.ChainDir,
		NetParams:      cfg.ActiveNetParams.Params,
		Wallet:         partialChainControl.Cfg.WalletUnlockParams.Wallet,
		Loader:         partialChainControl.Cfg.WalletUnlockParams.Loader,
		BlockCache:     cfg.BlockCache,
		DB:             cfg.FullDB,
	}

	walletController, err := dcrwallet.New(*dcrwConfig)
	if err != nil {
		err := fmt.Errorf("unable to create wallet controller: %v\n", err)
		return nil, nil, err
	}

	// When using dcrwallet mode, set the the chainIO to be the wallet
	// itself.
	if chainIO == nil {
		chainIO = walletController
	}

	// Create, and start the lnwallet, which handles the core payment
	// channel logic, and exposes control via proxy state machines.
	lnWalletConfig := lnwallet.Config{
		Database:           partialChainControl.Cfg.ChanStateDB,
		Notifier:           partialChainControl.ChainNotifier,
		WalletController:   walletController,
		Signer:             walletController,
		FeeEstimator:       partialChainControl.FeeEstimator,
		SecretKeyRing:      walletController,
		ChainIO:            chainIO,
		DefaultConstraints: partialChainControl.ChannelConstraints,
		NetParams:          *partialChainControl.Cfg.ActiveNetParams.Params,
	}

	// We've created the wallet configuration now, so we can finish
	// initializing the main chain control.
	activeChainControl, cleanUp, err := chainreg.NewChainControl(
		lnWalletConfig, walletController, partialChainControl,
	)
	if err != nil {
		err := fmt.Errorf("unable to create chain control: %v", err)
		d.logger.Error(err)
		return nil, nil, err
	}

	return activeChainControl, cleanUp, nil
}

// DatabaseInstances is a struct that holds all instances to the actual
// databases that are used in lnd.
type DatabaseInstances struct {
	// GraphDB is the database that stores the channel graph used for path
	// finding.
	//
	// NOTE/TODO: This currently _needs_ to be the same instance as the
	// ChanStateDB below until the separation of the two databases is fully
	// complete!
	GraphDB *channeldb.DB

	// ChanStateDB is the database that stores all of our node's channel
	// state.
	//
	// NOTE/TODO: This currently _needs_ to be the same instance as the
	// GraphDB above until the separation of the two databases is fully
	// complete!
	ChanStateDB *channeldb.DB

	// HeightHintDB is the database that stores height hints for spends.
	HeightHintDB kvdb.Backend

	// MacaroonDB is the database that stores macaroon root keys.
	MacaroonDB kvdb.Backend

	// DecayedLogDB is the database that stores p2p related encryption
	// information.
	DecayedLogDB kvdb.Backend

	// TowerClientDB is the database that stores the watchtower client's
	// configuration.
	TowerClientDB wtclient.DB

	// TowerServerDB is the database that stores the watchtower server's
	// configuration.
	TowerServerDB watchtower.DB

	// WalletDB is the configuration for loading the wallet database using
	// the btcwallet's loader.
	WalletDB walletloader.LoaderOption
}

// DefaultDatabaseBuilder is a type that builds the default database backends
// for lnd, using the given configuration to decide what actual implementation
// to use.
type DefaultDatabaseBuilder struct {
	cfg    *Config
	logger slog.Logger
}

// NewDefaultDatabaseBuilder returns a new instance of the default database
// builder.
func NewDefaultDatabaseBuilder(cfg *Config,
	logger slog.Logger) *DefaultDatabaseBuilder {

	return &DefaultDatabaseBuilder{
		cfg:    cfg,
		logger: logger,
	}
}

// BuildDatabase extracts the current databases that we'll use for normal
// operation in the daemon. A function closure that closes all opened databases
// is also returned.
func (d *DefaultDatabaseBuilder) BuildDatabase(
	ctx context.Context) (*DatabaseInstances, func(), error) {

	d.logger.Infof("Opening the main database, this might take a few " +
		"minutes...")

	cfg := d.cfg
	if cfg.DB.Backend == lncfg.BoltBackend {
		d.logger.Infof("Opening bbolt database, sync_freelist=%v, "+
			"auto_compact=%v", !cfg.DB.Bolt.NoFreelistSync,
			cfg.DB.Bolt.AutoCompact)
	}

	startOpenTime := time.Now()

	databaseBackends, err := cfg.DB.GetBackends(
		ctx, cfg.graphDatabaseDir(), cfg.networkDir, filepath.Join(
			cfg.Watchtower.TowerDir,
			cfg.registeredChains.PrimaryChain().String(),
			lncfg.NormalizeNetwork(cfg.ActiveNetParams.Name),
		), cfg.WtClient.Active, cfg.Watchtower.Active,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to obtain database "+
			"backends: %v", err)
	}

	// With the full remote mode we made sure both the graph and channel
	// state DB point to the same local or remote DB and the same namespace
	// within that DB.
	dbs := &DatabaseInstances{
		HeightHintDB: databaseBackends.HeightHintDB,
		MacaroonDB:   databaseBackends.MacaroonDB,
		DecayedLogDB: databaseBackends.DecayedLogDB,
		WalletDB:     databaseBackends.WalletDB,
	}
	cleanUp := func() {
		// We can just close the returned close functions directly. Even
		// if we decorate the channel DB with an additional struct, its
		// close function still just points to the kvdb backend.
		for name, closeFunc := range databaseBackends.CloseFuncs {
			if err := closeFunc(); err != nil {
				d.logger.Errorf("Error closing %s "+
					"database: %v", name, err)
			}
		}
	}
	if databaseBackends.Remote {
		d.logger.Infof("Using remote %v database! Creating "+
			"graph and channel state DB instances", cfg.DB.Backend)
	} else {
		d.logger.Infof("Creating local graph and channel state DB " +
			"instances")
	}

	dbOptions := []channeldb.OptionModifier{
		channeldb.OptionSetRejectCacheSize(cfg.Caches.RejectCacheSize),
		channeldb.OptionSetChannelCacheSize(cfg.Caches.ChannelCacheSize),
		channeldb.OptionSetBatchCommitInterval(cfg.DB.BatchCommitInterval),
		channeldb.OptionDryRunMigration(cfg.DryRunMigration),
		channeldb.OptionSetUseGraphCache(!cfg.DB.NoGraphCache),
	}

	// We want to pre-allocate the channel graph cache according to what we
	// expect for mainnet to speed up memory allocation.
	if cfg.ActiveNetParams.Name == chaincfg.MainNetParams().Name {
		dbOptions = append(
			dbOptions, channeldb.OptionSetPreAllocCacheNumNodes(
				channeldb.DefaultPreAllocCacheNumNodes,
			),
		)
	}

	// Otherwise, we'll open two instances, one for the state we only need
	// locally, and the other for things we want to ensure are replicated.
	dbs.GraphDB, err = channeldb.CreateWithBackend(
		databaseBackends.GraphDB, dbOptions...,
	)
	switch {
	// Give the DB a chance to dry run the migration. Since we know that
	// both the channel state and graph DBs are still always behind the same
	// backend, we know this would be applied to both of those DBs.
	case err == channeldb.ErrDryRunMigrationOK:
		d.logger.Infof("Graph DB dry run migration successful")
		return nil, nil, err

	case err != nil:
		cleanUp()

		err := fmt.Errorf("unable to open graph DB: %v", err)
		d.logger.Error(err)
		return nil, nil, err
	}

	// For now, we don't _actually_ split the graph and channel state DBs on
	// the code level. Since they both are based upon the *channeldb.DB
	// struct it will require more refactoring to fully separate them. With
	// the full remote mode we at least know for now that they both point to
	// the same DB backend (and also namespace within that) so we only need
	// to apply any migration once.
	//
	// TODO(guggero): Once the full separation of anything graph related
	// from the channeldb.DB is complete, the decorated instance of the
	// channel state DB should be created here individually instead of just
	// using the same struct (and DB backend) instance.
	dbs.ChanStateDB = dbs.GraphDB

	// Wrap the watchtower client DB and make sure we clean up.
	if cfg.WtClient.Active {
		dbs.TowerClientDB, err = wtdb.OpenClientDB(
			databaseBackends.TowerClientDB,
		)
		if err != nil {
			cleanUp()

			err := fmt.Errorf("unable to open %s database: %v",
				lncfg.NSTowerClientDB, err)
			d.logger.Error(err)
			return nil, nil, err
		}
	}

	// Wrap the watchtower server DB and make sure we clean up.
	if cfg.Watchtower.Active {
		dbs.TowerServerDB, err = wtdb.OpenTowerDB(
			databaseBackends.TowerServerDB,
		)
		if err != nil {
			cleanUp()

			err := fmt.Errorf("unable to open %s database: %v",
				lncfg.NSTowerServerDB, err)
			d.logger.Error(err)
			return nil, nil, err
		}
	}

	openTime := time.Since(startOpenTime)
	d.logger.Infof("Database(s) now open (time_to_open=%v)!", openTime)

	return dbs, cleanUp, nil
}

// waitForWalletPassword blocks until a password is provided by the user to
// this RPC server.
func waitForWalletPassword(cfg *Config,
	pwService *walletunlocker.UnlockerService,
	loaderOpts []walletloader.LoaderOption, shutdownChan <-chan struct{}) (
	*walletunlocker.WalletUnlockParams, error) {

	ctx := context.TODO()

	// Wait for user to provide the password.
	ltndLog.Infof("Waiting for wallet encryption password. Use `lncli " +
		"create` to create a wallet, `lncli unlock` to unlock an " +
		"existing wallet, or `lncli changepassword` to change the " +
		"password of an existing wallet and unlock it.")

	// We currently don't distinguish between getting a password to be used
	// for creation or unlocking, as a new wallet db will be created if
	// none exists when creating the chain control.
	select {

	// The wallet is being created for the first time, we'll check to see
	// if the user provided any entropy for seed creation. If so, then
	// we'll create the wallet early to load the seed.
	case initMsg := <-pwService.InitMsgs:
		password := initMsg.Passphrase
		cipherSeed := initMsg.WalletSeed
		extendedKey := initMsg.WalletExtendedKey
		recoveryWindow := initMsg.RecoveryWindow

		// Before we proceed, we'll check the internal version of the
		// seed. If it's greater than the current key derivation
		// version, then we'll return an error as we don't understand
		// this.
		const latestVersion = keychain.KeyDerivationVersion
		if cipherSeed != nil &&
			cipherSeed.InternalVersion != latestVersion {

			return nil, fmt.Errorf("invalid internal "+
				"seed version %v, current version is %v",
				cipherSeed.InternalVersion,
				keychain.KeyDerivationVersion)
		}

		netDir := dcrwallet.NetworkDir(
			cfg.Decred.ChainDir, cfg.ActiveNetParams.Params,
		)
		loader := walletloader.NewLoader(
			cfg.ActiveNetParams.Params, netDir, recoveryWindow,
			// loaderOpts...,
		)

		// With the seed, we can now use the wallet loader to create
		// the wallet, then pass it back to avoid unlocking it again.
		var (
			birthday  time.Time
			newWallet *wallet.Wallet
			err       error
		)
		switch {
		// A normal cipher seed was given, use the birthday encoded in
		// it and create the wallet from that.
		case cipherSeed != nil:
			birthday = cipherSeed.BirthdayTime()
			newWallet, err = loader.CreateNewWallet(
				ctx, password, password, cipherSeed.Entropy[:],
			)

		// No seed was given, we're importing a wallet from its extended
		// private key.
		case extendedKey != nil:
			birthday = initMsg.ExtendedKeyBirthday
			newWallet, err = loader.CreateNewWalletExtendedKey(
				ctx, password, password, extendedKey,
			)

		default:
			// The unlocker service made sure either the cipher seed
			// or the extended key is set so, we shouldn't get here.
			// The default case is just here for readability and
			// completeness.
			err = fmt.Errorf("cannot create wallet, neither seed " +
				"nor extended key was given")
		}
		if err != nil {
			// Don't leave the file open in case the new wallet
			// could not be created for whatever reason.
			if err := loader.UnloadWallet(); err != nil {
				ltndLog.Errorf("Could not unload new "+
					"wallet: %v", err)
			}
			return nil, err
		}

		// For new wallets, the ResetWalletTransactions flag is a no-op.
		if cfg.resetWalletTransactions {
			ltndLog.Warnf("Ignoring reset-wallet-transactions " +
				"flag for new wallet as it has no effect")
		}

		return &walletunlocker.WalletUnlockParams{
			Password:        password,
			Birthday:        birthday,
			RecoveryWindow:  recoveryWindow,
			Wallet:          newWallet,
			Loader:          loader,
			ChansToRestore:  initMsg.ChanBackups,
			UnloadWallet:    loader.UnloadWallet,
			StatelessInit:   initMsg.StatelessInit,
			MacResponseChan: pwService.MacResponseChan,
		}, nil

	// The wallet has already been created in the past, and is simply being
	// unlocked. So we'll just return these passphrases.
	case unlockMsg := <-pwService.UnlockMsgs:
		// Resetting the transactions is something the user likely only
		// wants to do once so we add a prominent warning to the log to
		// remind the user to turn off the setting again after
		// successful completion.
		if cfg.resetWalletTransactions {
			ltndLog.Warnf("Dropped all transaction history from " +
				"on-chain wallet. Remember to disable " +
				"reset-wallet-transactions flag for next " +
				"start of lnd")
		}

		return &walletunlocker.WalletUnlockParams{
			Password:        unlockMsg.Passphrase,
			RecoveryWindow:  unlockMsg.RecoveryWindow,
			Wallet:          unlockMsg.Wallet,
			Loader:          unlockMsg.Loader,
			Conn:            unlockMsg.Conn,
			ChansToRestore:  unlockMsg.ChanBackups,
			UnloadWallet:    unlockMsg.UnloadWallet,
			StatelessInit:   unlockMsg.StatelessInit,
			MacResponseChan: pwService.MacResponseChan,
		}, nil

	// If we got a shutdown signal we just return with an error immediately
	case <-shutdownChan:
		return nil, fmt.Errorf("shutting down")
	}
}
