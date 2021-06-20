// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2019 The Decred developers
// Copyright (C) 2015-2020 The Lightning Network Developers

package dcrlnd

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/autopilot"
	"github.com/decred/dcrlnd/build"
	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/chanbackup"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/discovery"
	"github.com/decred/dcrlnd/funding"
	"github.com/decred/dcrlnd/htlcswitch"
	"github.com/decred/dcrlnd/htlcswitch/hodl"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lncfg"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/routerrpc"
	"github.com/decred/dcrlnd/lnrpc/signrpc"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/routing"
	"github.com/decred/dcrlnd/signal"
	"github.com/decred/dcrlnd/tor"
	flags "github.com/jessevdk/go-flags"
)

const (
	defaultDataDirname        = "data"
	defaultChainSubDirname    = "chain"
	defaultGraphSubDirname    = "graph"
	defaultTowerSubDirname    = "watchtower"
	defaultTLSCertFilename    = "tls.cert"
	defaultTLSKeyFilename     = "tls.key"
	defaultAdminMacFilename   = "admin.macaroon"
	defaultReadMacFilename    = "readonly.macaroon"
	defaultInvoiceMacFilename = "invoice.macaroon"
	defaultLogLevel           = "info"
	defaultLogDirname         = "logs"
	defaultLogFilename        = "lnd.log"
	defaultRPCPort            = 10009
	defaultRESTPort           = 8080
	defaultPeerPort           = 9735
	defaultRPCHost            = "localhost"

	defaultNoSeedBackup                  = false
	defaultPaymentsExpirationGracePeriod = time.Duration(0)
	defaultTrickleDelay                  = 90 * 1000
	defaultChanStatusSampleInterval      = time.Minute
	defaultChanEnableTimeout             = 19 * time.Minute
	defaultChanDisableTimeout            = 20 * time.Minute
	defaultHeightHintCacheQueryDisable   = false
	defaultMaxLogFiles                   = 3
	defaultMaxLogFileSize                = 10
	defaultMinBackoff                    = time.Second
	defaultMaxBackoff                    = time.Hour
	defaultLetsEncryptDirname            = "letsencrypt"
	defaultLetsEncryptListen             = ":80"

	defaultTorSOCKSPort            = 9050
	defaultTorDNSHost              = "soa.nodes.lightning.directory"
	defaultTorDNSPort              = 53
	defaultTorControlPort          = 9051
	defaultTorV2PrivateKeyFilename = "v2_onion_private_key"
	defaultTorV3PrivateKeyFilename = "v3_onion_private_key"

	// DefaultAutogenValidity is the default validity of a self-signed
	// certificate. The value corresponds to 14 months
	// (14 months * 30 days * 24 hours).
	defaultTLSCertDuration = 14 * 30 * 24 * time.Hour

	// minTimeLockDelta is the minimum timelock we require for incoming
	// HTLCs on our channels.
	minTimeLockDelta = routing.MinCLTVDelta

	// defaultAcceptorTimeout is the time after which an RPCAcceptor will time
	// out and return false if it hasn't yet received a response.
	defaultAcceptorTimeout = 15 * time.Second

	defaultAlias = ""
	defaultColor = "#3399FF"

	// defaultCoopCloseTargetConfs is the default confirmation target
	// that will be used to estimate a fee rate to use during a
	// cooperative channel closure initiated by a remote peer. By default
	// we'll set this to a lax value since we weren't the ones that
	// initiated the channel closure.
	defaultCoopCloseTargetConfs = 6

	// defaultBlockCacheSize is the size (in bytes) of blocks that will be
	// keep in memory if no size is specified.
	defaultBlockCacheSize uint64 = 20 * 1024 * 1024 // 20 MB

	// defaultHostSampleInterval is the default amount of time that the
	// HostAnnouncer will wait between DNS resolutions to check if the
	// backing IP of a host has changed.
	defaultHostSampleInterval = time.Minute * 5

	defaultChainInterval = time.Minute
	defaultChainTimeout  = time.Second * 30
	defaultChainBackoff  = time.Minute * 2
	defaultChainAttempts = 3

	// Set defaults for a health check which ensures that we have space
	// available on disk. Although this check is off by default so that we
	// avoid breaking any existing setups (particularly on mobile), we still
	// set the other default values so that the health check can be easily
	// enabled with sane defaults.
	defaultRequiredDisk = 0.1
	defaultDiskInterval = time.Hour * 12
	defaultDiskTimeout  = time.Second * 5
	defaultDiskBackoff  = time.Minute
	defaultDiskAttempts = 0

	// Set defaults for a health check which ensures that the TLS certificate
	// is not expired. Although this check is off by default (not all setups
	// require it), we still set the other default values so that the health
	// check can be easily enabled with sane defaults.
	defaultTLSInterval = time.Minute
	defaultTLSTimeout  = time.Second * 5
	defaultTLSBackoff  = time.Minute
	defaultTLSAttempts = 0

	// defaultRemoteMaxHtlcs specifies the default limit for maximum
	// concurrent HTLCs the remote party may add to commitment transactions.
	// This value can be overridden with --default-remote-max-htlcs.
	defaultRemoteMaxHtlcs = input.MaxHTLCNumber / 2

	// defaultMaxLocalCSVDelay is the maximum delay we accept on our
	// commitment output.
	// TODO(halseth): find a more scientific choice of value.
	defaultMaxLocalCSVDelay = 10000

	// defaultChannelCommitInterval is the default maximum time between receiving a
	// channel state update and signing a new commitment.
	defaultChannelCommitInterval = 50 * time.Millisecond

	// defaultChannelCommitBatchSize is the default maximum number of
	// channel state updates that is accumulated before signing a new
	// commitment.
	defaultChannelCommitBatchSize = 10

	// defaultCoinSelectionStrategy is the coin selection strategy that is
	// used by default to fund transactions.
	defaultCoinSelectionStrategy = "random"
)

var (
	// DefaultLndDir is the default directory where lnd tries to find its
	// configuration file and store its data. This is a directory in the
	// user's application data, for example:
	// C:\Users\<username>\AppData\Local\Lnd on Windows ~/.lnd on Linux
	// ~/Library/Application Support/Lnd on MacOS
	DefaultLndDir = dcrutil.AppDataDir("dcrlnd", false)

	// DefaultConfigFile is the default full path of lnd's configuration
	// file.
	DefaultConfigFile = filepath.Join(DefaultLndDir, lncfg.DefaultConfigFilename)

	defaultDataDir = filepath.Join(DefaultLndDir, defaultDataDirname)
	defaultLogDir  = filepath.Join(DefaultLndDir, defaultLogDirname)

	defaultTowerDir = filepath.Join(defaultDataDir, defaultTowerSubDirname)

	defaultTLSCertPath    = filepath.Join(DefaultLndDir, defaultTLSCertFilename)
	defaultTLSKeyPath     = filepath.Join(DefaultLndDir, defaultTLSKeyFilename)
	defaultLetsEncryptDir = filepath.Join(DefaultLndDir, defaultLetsEncryptDirname)

	defaultDcrdDir         = dcrutil.AppDataDir("dcrd", false)
	defaultDcrdRPCCertFile = filepath.Join(defaultDcrdDir, "rpc.cert")

	defaultTorSOCKS   = net.JoinHostPort("localhost", strconv.Itoa(defaultTorSOCKSPort))
	defaultTorDNS     = net.JoinHostPort(defaultTorDNSHost, strconv.Itoa(defaultTorDNSPort))
	defaultTorControl = net.JoinHostPort("localhost", strconv.Itoa(defaultTorControlPort))
)

// Config defines the configuration options for lnd.
//
// See LoadConfig for further details regarding the configuration
// loading+parsing process.
type Config struct {
	ShowVersion bool `short:"V" long:"version" description:"Display version information and exit"`

	LndDir       string `long:"lnddir" description:"The base directory that contains lnd's data, logs, configuration file, etc."`
	ConfigFile   string `short:"C" long:"configfile" description:"Path to configuration file"`
	DataDir      string `short:"b" long:"datadir" description:"The directory to store lnd's data within"`
	SyncFreelist bool   `long:"sync-freelist" description:"Whether the databases used within lnd should sync their freelist to disk. This is disabled by default resulting in improved memory performance during operation, but with an increase in startup time."`

	TLSCertPath        string        `long:"tlscertpath" description:"Path to write the TLS certificate for lnd's RPC and REST services"`
	TLSKeyPath         string        `long:"tlskeypath" description:"Path to write the TLS private key for lnd's RPC and REST services"`
	TLSExtraIPs        []string      `long:"tlsextraip" description:"Adds an extra ip to the generated certificate"`
	TLSExtraDomains    []string      `long:"tlsextradomain" description:"Adds an extra domain to the generated certificate"`
	TLSAutoRefresh     bool          `long:"tlsautorefresh" description:"Re-generate TLS certificate and key if the IPs or domains are changed"`
	TLSDisableAutofill bool          `long:"tlsdisableautofill" description:"Do not include the interface IPs or the system hostname in TLS certificate, use first --tlsextradomain as Common Name instead, if set"`
	TLSCertDuration    time.Duration `long:"tlscertduration" description:"The duration for which the auto-generated TLS certificate will be valid for"`

	NoMacaroons     bool          `long:"no-macaroons" description:"Disable macaroon authentication"`
	AdminMacPath    string        `long:"adminmacaroonpath" description:"Path to write the admin macaroon for lnd's RPC and REST services if it doesn't exist"`
	ReadMacPath     string        `long:"readonlymacaroonpath" description:"Path to write the read-only macaroon for lnd's RPC and REST services if it doesn't exist"`
	InvoiceMacPath  string        `long:"invoicemacaroonpath" description:"Path to the invoice-only macaroon for lnd's RPC and REST services if it doesn't exist"`
	LogDir          string        `long:"logdir" description:"Directory to log output."`
	MaxLogFiles     int           `long:"maxlogfiles" description:"Maximum logfiles to keep (0 for no rotation)"`
	MaxLogFileSize  int           `long:"maxlogfilesize" description:"Maximum logfile size in MB"`
	AcceptorTimeout time.Duration `long:"acceptortimeout" description:"Time after which an RPCAcceptor will time out and return false if it hasn't yet received a response"`

	// IPC options
	PipeTx            *uint `long:"pipetx" description:"File descriptor or handle of write end pipe to enable child -> parent process communication"`
	PipeRx            *uint `long:"piperx" description:"File descriptor or handle of read end pipe to enable parent -> child process communication"`
	RPCListenerEvents bool  `long:"rpclistenerevents" description:"Notify JSON-RPC and gRPC listener addresses over the TX pipe"`

	LetsEncryptDir    string `long:"letsencryptdir" description:"The directory to store Let's Encrypt certificates within"`
	LetsEncryptListen string `long:"letsencryptlisten" description:"The IP:port on which lnd will listen for Let's Encrypt challenges. Let's Encrypt will always try to contact on port 80. Often non-root processes are not allowed to bind to ports lower than 1024. This configuration option allows a different port to be used, but must be used in combination with port forwarding from port 80. This configuration can also be used to specify another IP address to listen on, for example an IPv6 address."`
	LetsEncryptDomain string `long:"letsencryptdomain" description:"Request a Let's Encrypt certificate for this domain. Note that the certicate is only requested and stored when the first rpc connection comes in."`

	// We'll parse these 'raw' string arguments into real net.Addrs in the
	// loadConfig function. We need to expose the 'raw' strings so the
	// command line library can access them.
	// Only the parsed net.Addrs should be used!
	RawRPCListeners   []string `long:"rpclisten" description:"Add an interface/port/socket to listen for RPC connections"`
	RawRESTListeners  []string `long:"restlisten" description:"Add an interface/port/socket to listen for REST connections"`
	RawListeners      []string `long:"listen" description:"Add an interface/port to listen for peer connections"`
	RawExternalIPs    []string `long:"externalip" description:"Add an ip:port to the list of local addresses we claim to listen on to peers. If a port is not specified, the default (9735) will be used regardless of other parameters"`
	ExternalHosts     []string `long:"externalhosts" description:"A set of hosts that should be periodically resolved to announce IPs for"`
	RPCListeners      []net.Addr
	RESTListeners     []net.Addr
	RestCORS          []string `long:"restcors" description:"Add an ip:port/hostname to allow cross origin access from. To allow all origins, set as \"*\"."`
	Listeners         []net.Addr
	ExternalIPs       []net.Addr
	DisableListen     bool          `long:"nolisten" description:"Disable listening for incoming peer connections"`
	DisableRest       bool          `long:"norest" description:"Disable REST API"`
	DisableRestTLS    bool          `long:"no-rest-tls" description:"Disable TLS for REST connections"`
	WSPingInterval    time.Duration `long:"ws-ping-interval" description:"The ping interval for REST based WebSocket connections, set to 0 to disable sending ping messages from the server side"`
	WSPongWait        time.Duration `long:"ws-pong-wait" description:"The time we wait for a pong response message on REST based WebSocket connections before the connection is closed as inactive"`
	NAT               bool          `long:"nat" description:"Toggle NAT traversal support (using either UPnP or NAT-PMP) to automatically advertise your external IP address to the network -- NOTE this does not support devices behind multiple NATs"`
	MinBackoff        time.Duration `long:"minbackoff" description:"Shortest backoff when reconnecting to persistent peers. Valid time units are {s, m, h}."`
	MaxBackoff        time.Duration `long:"maxbackoff" description:"Longest backoff when reconnecting to persistent peers. Valid time units are {s, m, h}."`
	ConnectionTimeout time.Duration `long:"connectiontimeout" description:"The timeout value for network connections. Valid time units are {ms, s, m, h}."`

	DebugLevel string `short:"d" long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <global-level>,<subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`

	CPUProfile string `long:"cpuprofile" description:"Write CPU profile to the specified file"`
	MemProfile string `long:"memprofile" description:"Write memory profile to the specified file"`

	Profile string `long:"profile" description:"Enable HTTP profiling on either a port or host:port"`

	UnsafeDisconnect   bool   `long:"unsafe-disconnect" description:"DEPRECATED: Allows the rpcserver to intentionally disconnect from peers with open channels. THIS FLAG WILL BE REMOVED IN THE FUTURE"`
	UnsafeReplay       bool   `long:"unsafe-replay" description:"Causes a link to replay the adds on its commitment txn after starting up, this enables testing of the sphinx replay logic."`
	MaxPendingChannels int    `long:"maxpendingchannels" description:"The maximum number of incoming pending channels permitted per peer."`
	BackupFilePath     string `long:"backupfilepath" description:"The target location of the channel backup file"`

	FeeURL string `long:"feeurl" description:"Optional URL for external fee estimation. If no URL is specified, the method for fee estimation will depend on the chosen backend and network."`

	Decred    *lncfg.Chain
	DcrdMode  *lncfg.DcrdConfig      `group:"dcrd" namespace:"dcrd"`
	Dcrwallet *lncfg.DcrwalletConfig `group:"dcrwallet" namespace:"dcrwallet"`

	BlockCacheSize uint64 `long:"blockcachesize" description:"The maximum capacity of the block cache"`

	Autopilot *lncfg.AutoPilot `group:"Autopilot" namespace:"autopilot"`

	Automation *lncfg.Automation `group:"Automation" namespace:"automation"`

	Tor *lncfg.Tor `group:"Tor" namespace:"tor"`

	SubRPCServers *subRPCServerConfigs `group:"subrpc"`

	Hodl *hodl.Config `group:"hodl" namespace:"hodl"`

	NoNetBootstrap bool `long:"nobootstrap" description:"If true, then automatic network bootstrapping will not be attempted."`

	NoSeedBackup             bool   `long:"noseedbackup" description:"If true, NO SEED WILL BE EXPOSED -- EVER, AND THE WALLET WILL BE ENCRYPTED USING THE DEFAULT PASSPHRASE. THIS FLAG IS ONLY FOR TESTING AND SHOULD NEVER BE USED ON MAINNET."`
	WalletUnlockPasswordFile string `long:"wallet-unlock-password-file" description:"The full path to a file (or pipe/device) that contains the password for unlocking the wallet; if set, no unlocking through RPC is possible and lnd will exit if no wallet exists or the password is incorrect; if wallet-unlock-allow-create is also set then lnd will ignore this flag if no wallet exists and allow a wallet to be created through RPC."`
	WalletUnlockAllowCreate  bool   `long:"wallet-unlock-allow-create" description:"Don't fail with an error if wallet-unlock-password-file is set but no wallet exists yet."`

	CoinSelectionStrategy string `long:"coin-selection-strategy" description:"The strategy to use for selecting coins for wallet transactions." choice:"random"`

	PaymentsExpirationGracePeriod time.Duration `long:"payments-expiration-grace-period" description:"A period to wait before force closing channels with outgoing htlcs that have timed-out and are a result of this node initiated payments."`
	TrickleDelay                  int           `long:"trickledelay" description:"Time in milliseconds between each release of announcements to the network"`
	ChanEnableTimeout             time.Duration `long:"chan-enable-timeout" description:"The duration that a peer connection must be stable before attempting to send a channel update to reenable or cancel a pending disables of the peer's channels on the network."`
	ChanDisableTimeout            time.Duration `long:"chan-disable-timeout" description:"The duration that must elapse after first detecting that an already active channel is actually inactive and sending channel update disabling it to the network. The pending disable can be canceled if the peer reconnects and becomes stable for chan-enable-timeout before the disable update is sent."`
	ChanStatusSampleInterval      time.Duration `long:"chan-status-sample-interval" description:"The polling interval between attempts to detect if an active channel has become inactive due to its peer going offline."`
	HeightHintCacheQueryDisable   bool          `long:"height-hint-cache-query-disable" description:"Disable queries from the height-hint cache to try to recover channels stuck in the pending close state. Disabling height hint queries may cause longer chain rescans, resulting in a performance hit. Unset this after channels are unstuck so you can get better performance again."`
	Alias                         string        `long:"alias" description:"The node alias. Used as a moniker by peers and intelligence services"`
	Color                         string        `long:"color" description:"The color of the node in hex format (i.e. '#3399FF'). Used to customize node appearance in intelligence services"`
	MinChanSize                   int64         `long:"minchansize" description:"The smallest channel size (in atoms) that we should accept. Incoming channels smaller than this will be rejected"`
	MaxChanSize                   int64         `long:"maxchansize" description:"The largest channel size (in atoms) that we should accept. Incoming channels larger than this will be rejected"`
	CoopCloseTargetConfs          uint32        `long:"coop-close-target-confs" description:"The target number of blocks that a cooperative channel close transaction should confirm in. This is used to estimate the fee to use as the lower bound during fee negotiation for the channel closure."`

	ChannelCommitInterval  time.Duration `long:"channel-commit-interval" description:"The maximum time that is allowed to pass between receiving a channel state update and signing the next commitment. Setting this to a longer duration allows for more efficient channel operations at the cost of latency."`
	ChannelCommitBatchSize uint32        `long:"channel-commit-batch-size" description:"The maximum number of channel state updates that is accumulated before signing a new commitment."`

	DefaultRemoteMaxHtlcs uint16 `long:"default-remote-max-htlcs" description:"The default max_htlc applied when opening or accepting channels. This value limits the number of concurrent HTLCs that the remote party can add to the commitment. The maximum possible value is 300."`

	NumGraphSyncPeers      int           `long:"numgraphsyncpeers" description:"The number of peers that we should receive new graph updates from. This option can be tuned to save bandwidth for light clients or routing nodes."`
	HistoricalSyncInterval time.Duration `long:"historicalsyncinterval" description:"The polling interval between historical graph sync attempts. Each historical graph sync attempt ensures we reconcile with the remote peer's graph from the genesis block."`

	IgnoreHistoricalGossipFilters bool `long:"ignore-historical-gossip-filters" description:"If true, will not reply with historical data that matches the range specified by a remote peer's gossip_timestamp_filter. Doing so will result in lower memory and bandwidth requirements."`

	RejectPush bool `long:"rejectpush" description:"If true, lnd will not accept channel opening requests with non-zero push amounts. This should prevent accidental pushes to merchant nodes."`

	RejectHTLC bool `long:"rejecthtlc" description:"If true, lnd will not forward any HTLCs that are meant as onward payments. This option will still allow lnd to send HTLCs and receive HTLCs but lnd won't be used as a hop."`

	StaggerInitialReconnect bool `long:"stagger-initial-reconnect" description:"If true, will apply a randomized staggering between 0s and 30s when reconnecting to persistent peers on startup. The first 10 reconnections will be attempted instantly, regardless of the flag's value"`

	MaxOutgoingCltvExpiry uint32 `long:"max-cltv-expiry" description:"The maximum number of blocks funds could be locked up for when forwarding payments."`

	MaxChannelFeeAllocation float64 `long:"max-channel-fee-allocation" description:"The maximum percentage of total funds that can be allocated to a channel's commitment fee. This only applies for the initiator of the channel. Valid values are within [0.1, 1]."`

	MaxCommitFeeRateAnchors uint64 `long:"max-commit-fee-rate-anchors" description:"The maximum fee rate in sat/vbyte that will be used for commitments of channels of the anchors type. Must be large enough to ensure transaction propagation"`

	DryRunMigration bool `long:"dry-run-migration" description:"If true, lnd will abort committing a migration if it would otherwise have been successful. This leaves the database unmodified, and still compatible with the previously active version of lnd."`

	net tor.Net

	EnableUpfrontShutdown bool `long:"enable-upfront-shutdown" description:"If true, option upfront shutdown script will be enabled. If peers that we open channels with support this feature, we will automatically set the script to which cooperative closes should be paid out to on channel open. This offers the partial protection of a channel peer disconnecting from us if cooperative close is attempted with a different script."`

	AcceptKeySend bool `long:"accept-keysend" description:"If true, spontaneous payments through keysend will be accepted. [experimental]"`

	AcceptAMP bool `long:"accept-amp" description:"If true, spontaneous payments via AMP will be accepted."`

	KeysendHoldTime time.Duration `long:"keysend-hold-time" description:"If non-zero, keysend payments are accepted but not immediately settled. If the payment isn't settled manually after the specified time, it is canceled automatically. [experimental]"`

	GcCanceledInvoicesOnStartup bool `long:"gc-canceled-invoices-on-startup" description:"If true, we'll attempt to garbage collect canceled invoices upon start."`

	GcCanceledInvoicesOnTheFly bool `long:"gc-canceled-invoices-on-the-fly" description:"If true, we'll delete newly canceled invoices on the fly."`

	DustThreshold uint64 `long:"dust-threshold" description:"Sets the dust sum threshold in satoshis for a channel after which dust HTLC's will be failed."`

	Invoices *lncfg.Invoices `group:"invoices" namespace:"invoices"`

	Routing *lncfg.Routing `group:"routing" namespace:"routing"`

	Gossip *lncfg.Gossip `group:"gossip" namespace:"gossip"`

	Workers *lncfg.Workers `group:"workers" namespace:"workers"`

	Caches *lncfg.Caches `group:"caches" namespace:"caches"`

	Prometheus lncfg.Prometheus `group:"prometheus" namespace:"prometheus"`

	WtClient *lncfg.WtClient `group:"wtclient" namespace:"wtclient"`

	Watchtower *lncfg.Watchtower `group:"watchtower" namespace:"watchtower"`

	ProtocolOptions *lncfg.ProtocolOptions `group:"protocol" namespace:"protocol"`

	AllowCircularRoute bool `long:"allow-circular-route" description:"If true, our node will allow htlc forwards that arrive and depart on the same channel."`

	HealthChecks *lncfg.HealthCheckConfig `group:"healthcheck" namespace:"healthcheck"`

	DB *lncfg.DB `group:"db" namespace:"db"`

	Cluster *lncfg.Cluster `group:"cluster" namespace:"cluster"`

	// LogWriter is the root logger that all of the daemon's subloggers are
	// hooked up to.
	LogWriter *build.RotatingLogWriter

	// registeredChains keeps track of all chains that have been registered
	// with the daemon.
	registeredChains *chainreg.ChainRegistry

	// networkDir is the path to the directory of the currently active
	// network. This path will hold the files related to each different
	// network.
	networkDir string

	// ActiveNetParams contains parameters of the target chain.
	ActiveNetParams chainreg.DecredNetParams
}

// DefaultConfig returns all default values for the Config struct.
func DefaultConfig() Config {
	return Config{
		LndDir:            DefaultLndDir,
		ConfigFile:        DefaultConfigFile,
		DataDir:           defaultDataDir,
		DebugLevel:        defaultLogLevel,
		TLSCertPath:       defaultTLSCertPath,
		TLSKeyPath:        defaultTLSKeyPath,
		TLSCertDuration:   defaultTLSCertDuration,
		LetsEncryptDir:    defaultLetsEncryptDir,
		LetsEncryptListen: defaultLetsEncryptListen,
		LogDir:            defaultLogDir,
		MaxLogFiles:       defaultMaxLogFiles,
		MaxLogFileSize:    defaultMaxLogFileSize,
		AcceptorTimeout:   defaultAcceptorTimeout,
		WSPingInterval:    lnrpc.DefaultPingInterval,
		WSPongWait:        lnrpc.DefaultPongWait,
		Decred: &lncfg.Chain{
			MinHTLCIn:     chainreg.DefaultDecredMinHTLCInMAtoms,
			MinHTLCOut:    chainreg.DefaultDecredMinHTLCOutMAtoms,
			BaseFee:       chainreg.DefaultDecredBaseFeeMAtoms,
			FeeRate:       chainreg.DefaultDecredFeeRate,
			TimeLockDelta: chainreg.DefaultDecredTimeLockDelta,
			MaxLocalDelay: defaultMaxLocalCSVDelay,
			Node:          "dcrd",
		},
		DcrdMode: &lncfg.DcrdConfig{
			RPCHost: defaultRPCHost,
			RPCCert: defaultDcrdRPCCertFile,
		},
		Dcrwallet:          &lncfg.DcrwalletConfig{},
		BlockCacheSize:     defaultBlockCacheSize,
		UnsafeDisconnect:   true,
		MaxPendingChannels: lncfg.DefaultMaxPendingChannels,
		NoSeedBackup:       defaultNoSeedBackup,
		MinBackoff:         defaultMinBackoff,
		MaxBackoff:         defaultMaxBackoff,
		ConnectionTimeout:  tor.DefaultConnTimeout,
		SubRPCServers: &subRPCServerConfigs{
			SignRPC:   &signrpc.Config{},
			RouterRPC: routerrpc.DefaultConfig(),
		},
		Autopilot: &lncfg.AutoPilot{
			MaxChannels:    5,
			Allocation:     0.6,
			MinChannelSize: int64(funding.MinChanFundingSize),
			MaxChannelSize: int64(MaxFundingAmount),
			MinConfs:       1,
			ConfTarget:     autopilot.DefaultConfTarget,
			Heuristic: map[string]float64{
				"top_centrality": 1.0,
			},
		},
		Automation: &lncfg.Automation{
			ForceCloseChanReestablishWait: 60 * 60 * 24 * 3, // 3 days
		},
		PaymentsExpirationGracePeriod: defaultPaymentsExpirationGracePeriod,
		TrickleDelay:                  defaultTrickleDelay,
		ChanStatusSampleInterval:      defaultChanStatusSampleInterval,
		ChanEnableTimeout:             defaultChanEnableTimeout,
		ChanDisableTimeout:            defaultChanDisableTimeout,
		HeightHintCacheQueryDisable:   defaultHeightHintCacheQueryDisable,
		Alias:                         defaultAlias,
		Color:                         defaultColor,
		MinChanSize:                   int64(funding.MinChanFundingSize),
		MaxChanSize:                   int64(0),
		CoopCloseTargetConfs:          defaultCoopCloseTargetConfs,
		DefaultRemoteMaxHtlcs:         defaultRemoteMaxHtlcs,
		NumGraphSyncPeers:             defaultMinPeers,
		HistoricalSyncInterval:        discovery.DefaultHistoricalSyncInterval,
		Tor: &lncfg.Tor{
			SOCKS:   defaultTorSOCKS,
			DNS:     defaultTorDNS,
			Control: defaultTorControl,
		},
		net: &tor.ClearNet{},
		Workers: &lncfg.Workers{
			Read:  lncfg.DefaultReadWorkers,
			Write: lncfg.DefaultWriteWorkers,
			Sig:   lncfg.DefaultSigWorkers,
		},
		Caches: &lncfg.Caches{
			RejectCacheSize:  channeldb.DefaultRejectCacheSize,
			ChannelCacheSize: channeldb.DefaultChannelCacheSize,
		},
		Prometheus: lncfg.DefaultPrometheus(),
		Watchtower: &lncfg.Watchtower{
			TowerDir: defaultTowerDir,
		},
		HealthChecks: &lncfg.HealthCheckConfig{
			ChainCheck: &lncfg.CheckConfig{
				Interval: defaultChainInterval,
				Timeout:  defaultChainTimeout,
				Attempts: defaultChainAttempts,
				Backoff:  defaultChainBackoff,
			},
			DiskCheck: &lncfg.DiskCheckConfig{
				RequiredRemaining: defaultRequiredDisk,
				CheckConfig: &lncfg.CheckConfig{
					Interval: defaultDiskInterval,
					Attempts: defaultDiskAttempts,
					Timeout:  defaultDiskTimeout,
					Backoff:  defaultDiskBackoff,
				},
			},
			TLSCheck: &lncfg.CheckConfig{
				Interval: defaultTLSInterval,
				Timeout:  defaultTLSTimeout,
				Attempts: defaultTLSAttempts,
				Backoff:  defaultTLSBackoff,
			},
		},
		Routing: &lncfg.Routing{
			AssumeChannelValid: false,
		},
		Gossip: &lncfg.Gossip{
			MaxChannelUpdateBurst: discovery.DefaultMaxChannelUpdateBurst,
			ChannelUpdateInterval: discovery.DefaultChannelUpdateInterval,
		},
		Invoices: &lncfg.Invoices{
			HoldExpiryDelta: lncfg.DefaultHoldInvoiceExpiryDelta,
		},
		MaxOutgoingCltvExpiry:   htlcswitch.DefaultMaxOutgoingCltvExpiry,
		MaxChannelFeeAllocation: htlcswitch.DefaultMaxLinkFeeAllocation,
		MaxCommitFeeRateAnchors: lnwallet.DefaultAnchorsCommitMaxFeeRateAtomsPerByte,
		DustThreshold:           uint64(htlcswitch.DefaultDustThreshold.ToAtoms()),
		LogWriter:               build.NewRotatingLogWriter(),
		DB:                      lncfg.DefaultDB(),
		Cluster:                 lncfg.DefaultCluster(),
		registeredChains:        chainreg.NewChainRegistry(),
		ActiveNetParams:         chainreg.DecredTestNetParams,
		ChannelCommitInterval:   defaultChannelCommitInterval,
		ChannelCommitBatchSize:  defaultChannelCommitBatchSize,
		CoinSelectionStrategy:   defaultCoinSelectionStrategy,
	}
}

// LoadConfig initializes and parses the config using a config file and command
// line options.
//
// The configuration proceeds as follows:
//  1. Start with a default config with sane settings
//  2. Pre-parse the command line to check for an alternative config file
//  3. Load configuration file overwriting defaults with any specified options
//  4. Parse CLI options and overwrite/add any specified options
func LoadConfig(interceptor signal.Interceptor) (*Config, error) {
	// Pre-parse the command line options to pick up an alternative config
	// file.
	preCfg := DefaultConfig()
	if _, err := flags.Parse(&preCfg); err != nil {
		return nil, err
	}

	// Show the version and exit if the version flag was specified.
	appName := filepath.Base(os.Args[0])
	appName = strings.TrimSuffix(appName, filepath.Ext(appName))
	usageMessage := fmt.Sprintf("Use %s -h to show usage", appName)
	if preCfg.ShowVersion {
		commit := build.SourceCommit()
		if commit != "" {
			commit = fmt.Sprintf("Commit %s; ", commit)
		}
		fmt.Printf("%s version %s (%sGo version %s %s/%s)\n",
			appName, build.Version(), commit,
			runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// If the config file path has not been modified by the user, then we'll
	// use the default config file path. However, if the user has modified
	// their lnddir, then we should assume they intend to use the config
	// file within it.
	configFileDir := CleanAndExpandPath(preCfg.LndDir)
	configFilePath := CleanAndExpandPath(preCfg.ConfigFile)
	if configFileDir != DefaultLndDir {
		if configFilePath == DefaultConfigFile {
			configFilePath = filepath.Join(
				configFileDir, lncfg.DefaultConfigFilename,
			)
		}
	}

	// Next, load any additional configuration options from the file.
	var configFileError error
	cfg := preCfg
	if err := flags.IniParse(configFilePath, &cfg); err != nil {
		// If it's a parsing related error, then we'll return
		// immediately, otherwise we can proceed as possibly the config
		// file doesn't exist which is OK.
		if _, ok := err.(*flags.IniError); ok {
			return nil, err
		}

		configFileError = err
	}

	// Finally, parse the remaining command line options again to ensure
	// they take precedence.
	if _, err := flags.Parse(&cfg); err != nil {
		return nil, err
	}

	// Make sure everything we just loaded makes sense.
	cleanCfg, err := ValidateConfig(cfg, usageMessage, interceptor)
	if err != nil {
		return nil, err
	}

	// Warn about missing config file only after all other configuration is
	// done.  This prevents the warning on help messages and invalid
	// options.  Note this should go directly before the return.
	if configFileError != nil {
		ltndLog.Warnf("%v", configFileError)
	}

	return cleanCfg, nil
}

// ValidateConfig check the given configuration to be sane. This makes sure no
// illegal values or combination of values are set. All file system paths are
// normalized. The cleaned up config is returned on success.
func ValidateConfig(cfg Config, usageMessage string,
	interceptor signal.Interceptor) (*Config, error) {
	// If the provided lnd directory is not the default, we'll modify the
	// path to all of the files and directories that will live within it.
	lndDir := CleanAndExpandPath(cfg.LndDir)
	if lndDir != DefaultLndDir {
		cfg.DataDir = filepath.Join(lndDir, defaultDataDirname)
		cfg.LetsEncryptDir = filepath.Join(
			lndDir, defaultLetsEncryptDirname,
		)
		cfg.TLSCertPath = filepath.Join(lndDir, defaultTLSCertFilename)
		cfg.TLSKeyPath = filepath.Join(lndDir, defaultTLSKeyFilename)
		cfg.LogDir = filepath.Join(lndDir, defaultLogDirname)

		// If the watchtower's directory is set to the default, i.e. the
		// user has not requested a different location, we'll move the
		// location to be relative to the specified lnd directory.
		if cfg.Watchtower.TowerDir == defaultTowerDir {
			cfg.Watchtower.TowerDir =
				filepath.Join(cfg.DataDir, defaultTowerSubDirname)
		}
	}

	funcName := "loadConfig"
	makeDirectory := func(dir string) error {
		err := os.MkdirAll(dir, 0700)
		if err != nil {
			// Show a nicer error message if it's because a symlink
			// is linked to a directory that does not exist
			// (probably because it's not mounted).
			if e, ok := err.(*os.PathError); ok && os.IsExist(err) {
				link, lerr := os.Readlink(e.Path)
				if lerr == nil {
					str := "is symlink %s -> %s mounted?"
					err = fmt.Errorf(str, e.Path, link)
				}
			}

			str := "%s: Failed to create lnd directory: %v"
			err := fmt.Errorf(str, funcName, err)
			_, _ = fmt.Fprintln(os.Stderr, err)
			return err
		}

		return nil
	}

	// As soon as we're done parsing configuration options, ensure all paths
	// to directories and files are cleaned and expanded before attempting
	// to use them later on.
	cfg.DataDir = CleanAndExpandPath(cfg.DataDir)
	cfg.TLSCertPath = CleanAndExpandPath(cfg.TLSCertPath)
	cfg.TLSKeyPath = CleanAndExpandPath(cfg.TLSKeyPath)
	cfg.LetsEncryptDir = CleanAndExpandPath(cfg.LetsEncryptDir)
	cfg.AdminMacPath = CleanAndExpandPath(cfg.AdminMacPath)
	cfg.ReadMacPath = CleanAndExpandPath(cfg.ReadMacPath)
	cfg.InvoiceMacPath = CleanAndExpandPath(cfg.InvoiceMacPath)
	cfg.LogDir = CleanAndExpandPath(cfg.LogDir)
	cfg.Tor.PrivateKeyPath = CleanAndExpandPath(cfg.Tor.PrivateKeyPath)
	cfg.Tor.WatchtowerKeyPath = CleanAndExpandPath(cfg.Tor.WatchtowerKeyPath)
	cfg.Watchtower.TowerDir = CleanAndExpandPath(cfg.Watchtower.TowerDir)
	cfg.Dcrwallet.CertPath = CleanAndExpandPath(cfg.Dcrwallet.CertPath)
	cfg.Dcrwallet.ClientKeyPath = CleanAndExpandPath(cfg.Dcrwallet.ClientKeyPath)
	cfg.Dcrwallet.ClientCertPath = CleanAndExpandPath(cfg.Dcrwallet.ClientCertPath)
	cfg.BackupFilePath = CleanAndExpandPath(cfg.BackupFilePath)
	cfg.WalletUnlockPasswordFile = CleanAndExpandPath(
		cfg.WalletUnlockPasswordFile,
	)

	// Create the lnd directory and all other sub directories if they don't
	// already exist. This makes sure that directory trees are also created
	// for files that point to outside of the lnddir.
	dirs := []string{
		lndDir, cfg.DataDir,
		cfg.LetsEncryptDir, cfg.Watchtower.TowerDir,
		filepath.Dir(cfg.TLSCertPath), filepath.Dir(cfg.TLSKeyPath),
		filepath.Dir(cfg.AdminMacPath), filepath.Dir(cfg.ReadMacPath),
		filepath.Dir(cfg.InvoiceMacPath),
		filepath.Dir(cfg.Tor.PrivateKeyPath),
		filepath.Dir(cfg.Tor.WatchtowerKeyPath),
	}
	for _, dir := range dirs {
		if err := makeDirectory(dir); err != nil {
			return nil, err
		}
	}

	// Ensure that the user didn't attempt to specify negative values for
	// any of the autopilot params.
	if cfg.Autopilot.MaxChannels < 0 {
		str := "%s: autopilot.maxchannels must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.Allocation < 0 {
		str := "%s: autopilot.allocation must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinChannelSize < 0 {
		str := "%s: autopilot.minchansize must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MaxChannelSize < 0 {
		str := "%s: autopilot.maxchansize must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinConfs < 0 {
		str := "%s: autopilot.minconfs must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.ConfTarget < 1 {
		str := "%s: autopilot.conftarget must be positive"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	// Ensure that the specified values for the min and max channel size
	// are within the bounds of the normal chan size constraints.
	if cfg.Autopilot.MinChannelSize < int64(funding.MinChanFundingSize) {
		cfg.Autopilot.MinChannelSize = int64(funding.MinChanFundingSize)
	}
	if cfg.Autopilot.MaxChannelSize > int64(MaxFundingAmount) {
		cfg.Autopilot.MaxChannelSize = int64(MaxFundingAmount)
	}

	if _, err := validateAtplCfg(cfg.Autopilot); err != nil {
		return nil, err
	}

	// Ensure that --maxchansize is properly handled when set by user.  For
	// non-Wumbo channels this limit remains 1073741823 atoms by default as
	// specified in BOLT-02. For wumbo channels this limit is 500e8 atoms
	// (500 DCR). Always enforce --maxchansize explicitly set by user.  If
	// unset (marked by 0 value), then enforce proper default.
	if cfg.MaxChanSize == 0 {
		if cfg.ProtocolOptions.Wumbo() {
			cfg.MaxChanSize = int64(funding.MaxDecredFundingAmountWumbo)
		} else {
			cfg.MaxChanSize = int64(funding.MaxDecredFundingAmount)
		}
	}

	// Ensure that the user specified values for the min and max channel
	// size make sense.
	if cfg.MaxChanSize < cfg.MinChanSize {
		return nil, fmt.Errorf("invalid channel size parameters: "+
			"max channel size %v, must be no less than min chan size %v",
			cfg.MaxChanSize, cfg.MinChanSize,
		)
	}

	// Don't allow superflous --maxchansize greater than
	// BOLT 02 soft-limit for non-wumbo channel
	if !cfg.ProtocolOptions.Wumbo() && cfg.MaxChanSize > int64(MaxFundingAmount) {
		return nil, fmt.Errorf("invalid channel size parameters: "+
			"maximum channel size %v is greater than maximum non-wumbo"+
			" channel size %v",
			cfg.MaxChanSize, MaxFundingAmount,
		)
	}

	// Ensure a valid max channel fee allocation was set.
	if cfg.MaxChannelFeeAllocation <= 0 || cfg.MaxChannelFeeAllocation > 1 {
		return nil, fmt.Errorf("invalid max channel fee allocation: "+
			"%v, must be within (0, 1]",
			cfg.MaxChannelFeeAllocation)
	}

	if cfg.MaxCommitFeeRateAnchors < 1 {
		return nil, fmt.Errorf("invalid max commit fee rate anchors: "+
			"%v, must be at least 1 sat/vbyte",
			cfg.MaxCommitFeeRateAnchors)
	}

	// Validate the Tor config parameters.
	socks, err := lncfg.ParseAddressString(
		cfg.Tor.SOCKS, strconv.Itoa(defaultTorSOCKSPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}
	cfg.Tor.SOCKS = socks.String()

	// We'll only attempt to normalize and resolve the DNS host if it hasn't
	// changed, as it doesn't need to be done for the default.
	if cfg.Tor.DNS != defaultTorDNS {
		dns, err := lncfg.ParseAddressString(
			cfg.Tor.DNS, strconv.Itoa(defaultTorDNSPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}
		cfg.Tor.DNS = dns.String()
	}

	control, err := lncfg.ParseAddressString(
		cfg.Tor.Control, strconv.Itoa(defaultTorControlPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}
	cfg.Tor.Control = control.String()

	// Ensure that tor socks host:port is not equal to tor control
	// host:port. This would lead to lnd not starting up properly.
	if cfg.Tor.SOCKS == cfg.Tor.Control {
		str := "%s: tor.socks and tor.control can not use " +
			"the same host:port"
		return nil, fmt.Errorf(str, funcName)
	}

	switch {
	case cfg.Tor.V2 && cfg.Tor.V3:
		return nil, errors.New("either tor.v2 or tor.v3 can be set, " +
			"but not both")
	case cfg.DisableListen && (cfg.Tor.V2 || cfg.Tor.V3):
		return nil, errors.New("listening must be enabled when " +
			"enabling inbound connections over Tor")
	}

	if cfg.Tor.PrivateKeyPath == "" {
		switch {
		case cfg.Tor.V2:
			cfg.Tor.PrivateKeyPath = filepath.Join(
				lndDir, defaultTorV2PrivateKeyFilename,
			)
		case cfg.Tor.V3:
			cfg.Tor.PrivateKeyPath = filepath.Join(
				lndDir, defaultTorV3PrivateKeyFilename,
			)
		}
	}

	if cfg.Tor.WatchtowerKeyPath == "" {
		switch {
		case cfg.Tor.V2:
			cfg.Tor.WatchtowerKeyPath = filepath.Join(
				cfg.Watchtower.TowerDir, defaultTorV2PrivateKeyFilename,
			)
		case cfg.Tor.V3:
			cfg.Tor.WatchtowerKeyPath = filepath.Join(
				cfg.Watchtower.TowerDir, defaultTorV3PrivateKeyFilename,
			)
		}
	}

	// Set up the network-related functions that will be used throughout
	// the daemon. We use the standard Go "net" package functions by
	// default. If we should be proxying all traffic through Tor, then
	// we'll use the Tor proxy specific functions in order to avoid leaking
	// our real information.
	if cfg.Tor.Active {
		cfg.net = &tor.ProxyNet{
			SOCKS:             cfg.Tor.SOCKS,
			DNS:               cfg.Tor.DNS,
			StreamIsolation:   cfg.Tor.StreamIsolation,
			DirectConnections: cfg.Tor.DirectConnections,
		}
	}

	if cfg.DisableListen && cfg.NAT {
		return nil, errors.New("NAT traversal cannot be used when " +
			"listening is disabled")
	}
	if cfg.NAT && len(cfg.ExternalHosts) != 0 {
		return nil, errors.New("NAT support and externalhosts are " +
			"mutually exclusive, only one should be selected")
	}

	// Multiple networks can't be selected simultaneously.  Count number of
	// network flags passed; assign active network params while we're at
	// it.
	numNets := 0
	if cfg.Decred.TestNet3 {
		numNets++
		cfg.ActiveNetParams = chainreg.DecredTestNetParams
	}
	if cfg.Decred.RegTest {
		numNets++
		cfg.ActiveNetParams = chainreg.RegTestNetParams
	}
	if cfg.Decred.SimNet {
		numNets++
		cfg.ActiveNetParams = chainreg.DecredSimNetParams
	}
	if numNets > 1 {
		str := "%s: The testnet, regtest, and simnet params" +
			"can't be used together -- choose one of the three"
		err := fmt.Errorf(str, funcName)
		return nil, err
	}

	// We default to mainnet if none are specified.
	if numNets == 0 {
		cfg.ActiveNetParams = chainreg.DecredMainNetParams
	}

	err = cfg.Decred.Validate(minTimeLockDelta, funding.MinDcrRemoteDelay)
	if err != nil {
		return nil, err
	}

	switch cfg.Decred.Node {
	case "dcrd":
		err := parseRPCParams(
			cfg.DcrdMode, chainreg.DecredChain, cfg.Decred.SimNet,
			cfg.Decred.Node, funcName,
		)
		if err != nil {
			err := fmt.Errorf("unable to load RPC "+
				"credentials for dcrd: %v", err)
			return nil, err
		}
	case "dcrw":
		// In dcrw mode we use the underlying wallet for chain
		// operations.
	default:
		str := "%s: only dcrd and dcrw modes supported for Decred at " +
			"this time"
		return nil, fmt.Errorf(str, funcName)
	}

	cfg.Decred.ChainDir = filepath.Join(cfg.DataDir,
		defaultChainSubDirname,
		chainreg.DecredChain.String())

	// Ensure sane config when using a remote wallet.
	if cfg.Dcrwallet.GRPCHost != "" {
		// Update Dcrwallet.GRPCHost with correct port from
		// activeNetParam selected.
		_, _, err := net.SplitHostPort(cfg.Dcrwallet.GRPCHost)
		if err != nil {
			cfg.Dcrwallet.GRPCHost = net.JoinHostPort(
				cfg.Dcrwallet.GRPCHost,
				cfg.ActiveNetParams.DcrwPort,
			)
		}

		// dcrwallet v1.6.0+ requires a client cert and key for gRPC
		// auth, so if any of {ClientKeyPath, ClientCertPath} are
		// specified, *both* need to be.
		//
		// Otherwise, the client key and cert will come on a
		// WalletUnlocker.Unlock() call.
		clientKeyEmpty := cfg.Dcrwallet.ClientKeyPath == ""
		clientCertEmpty := cfg.Dcrwallet.ClientCertPath == ""
		if clientKeyEmpty != clientCertEmpty {
			str := "%s: dcrwallet.clientkeypath and clientcertpath must both be specified"
			err := fmt.Errorf(str, funcName)
			return nil, err
		}
	}

	// Finally we'll register the decred chain as our current
	// primary chain.
	cfg.registeredChains.RegisterPrimaryChain(chainreg.DecredChain)

	// Ensure that the user didn't attempt to specify negative values for
	// any of the autopilot params.
	if cfg.Autopilot.MaxChannels < 0 {
		str := "%s: autopilot.maxchannels must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.Allocation < 0 {
		str := "%s: autopilot.allocation must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinChannelSize < 0 {
		str := "%s: autopilot.minchansize must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MaxChannelSize < 0 {
		str := "%s: autopilot.maxchansize must be non-negative"
		err := fmt.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	// Ensure that the specified values for the min and max channel size
	// don't are within the bounds of the normal chan size constraints.
	if cfg.Autopilot.MinChannelSize < int64(funding.MinChanFundingSize) {
		cfg.Autopilot.MinChannelSize = int64(funding.MinChanFundingSize)
	}
	if cfg.Autopilot.MaxChannelSize > int64(MaxFundingAmount) {
		cfg.Autopilot.MaxChannelSize = int64(MaxFundingAmount)
	}

	// Validate profile port or host:port.
	if cfg.Profile != "" {
		str := "%s: The profile port must be between 1024 and 65535"

		// Try to parse Profile as a host:port.
		_, hostPort, err := net.SplitHostPort(cfg.Profile)
		if err == nil {
			// Determine if the port is valid.
			profilePort, err := strconv.Atoi(hostPort)
			if err != nil || profilePort < 1024 || profilePort > 65535 {
				err = fmt.Errorf(str, funcName)
				_, _ = fmt.Fprintln(os.Stderr, err)
				_, _ = fmt.Fprintln(os.Stderr, usageMessage)
				return nil, err
			}
		} else {
			// Try to parse Profile as a port.
			profilePort, err := strconv.Atoi(cfg.Profile)
			if err != nil || profilePort < 1024 || profilePort > 65535 {
				err = fmt.Errorf(str, funcName)
				_, _ = fmt.Fprintln(os.Stderr, err)
				_, _ = fmt.Fprintln(os.Stderr, usageMessage)
				return nil, err
			}

			// Since the user just set a port, we will serve debugging
			// information over localhost.
			cfg.Profile = net.JoinHostPort("127.0.0.1", cfg.Profile)
		}
	}

	// We'll now construct the network directory which will be where we
	// store all the data specific to this chain/network.
	cfg.networkDir = filepath.Join(
		cfg.DataDir, defaultChainSubDirname,
		cfg.registeredChains.PrimaryChain().String(),
		lncfg.NormalizeNetwork(cfg.ActiveNetParams.Name),
	)

	// We need to make sure the default network directory exists for when we
	// try to create our default macaroons there.
	if err := makeDirectory(cfg.networkDir); err != nil {
		return nil, err
	}

	// If a custom macaroon directory wasn't specified and the data
	// directory has changed from the default path, then we'll also update
	// the path for the macaroons to be generated.
	if cfg.AdminMacPath == "" {
		cfg.AdminMacPath = filepath.Join(
			cfg.networkDir, defaultAdminMacFilename,
		)
	}
	if cfg.ReadMacPath == "" {
		cfg.ReadMacPath = filepath.Join(
			cfg.networkDir, defaultReadMacFilename,
		)
	}
	if cfg.InvoiceMacPath == "" {
		cfg.InvoiceMacPath = filepath.Join(
			cfg.networkDir, defaultInvoiceMacFilename,
		)
	}

	// Similarly, if a custom back up file path wasn't specified, then
	// we'll update the file location to match our set network directory.
	if cfg.BackupFilePath == "" {
		cfg.BackupFilePath = filepath.Join(
			cfg.networkDir, chanbackup.DefaultBackupFileName,
		)
	}

	// Append the network type to the log directory so it is "namespaced"
	// per network in the same fashion as the data directory.
	cfg.LogDir = filepath.Join(cfg.LogDir,
		cfg.registeredChains.PrimaryChain().String(),
		lncfg.NormalizeNetwork(cfg.ActiveNetParams.Name))

	// A log writer must be passed in, otherwise we can't function and would
	// run into a panic later on.
	if cfg.LogWriter == nil {
		return nil, fmt.Errorf("log writer missing in config")
	}

	// Special show command to list supported subsystems and exit.
	if cfg.DebugLevel == "show" {
		fmt.Println("Supported subsystems",
			cfg.LogWriter.SupportedSubsystems())
		os.Exit(0)
	}

	// Initialize logging at the default logging level.
	SetupLoggers(cfg.LogWriter, interceptor)
	err = cfg.LogWriter.InitLogRotator(
		filepath.Join(cfg.LogDir, defaultLogFilename),
		cfg.MaxLogFileSize, cfg.MaxLogFiles,
	)
	if err != nil {
		str := "%s: log rotation setup failed: %v"
		err = fmt.Errorf(str, funcName, err.Error())
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	// Parse, validate, and set debug log level(s).
	err = build.ParseAndSetDebugLevels(cfg.DebugLevel, cfg.LogWriter)
	if err != nil {
		err = fmt.Errorf("%s: %v", funcName, err.Error())
		_, _ = fmt.Fprintln(os.Stderr, err)
		_, _ = fmt.Fprintln(os.Stderr, usageMessage)
		return nil, err
	}

	// At least one RPCListener is required. So listen on localhost per
	// default.
	if len(cfg.RawRPCListeners) == 0 {
		addr := fmt.Sprintf("localhost:%d", defaultRPCPort)
		cfg.RawRPCListeners = append(cfg.RawRPCListeners, addr)
	}

	// Listen on localhost if no REST listeners were specified.
	if len(cfg.RawRESTListeners) == 0 {
		addr := fmt.Sprintf("localhost:%d", defaultRESTPort)
		cfg.RawRESTListeners = append(cfg.RawRESTListeners, addr)
	}

	// Listen on the default interface/port if no listeners were specified.
	// An empty address string means default interface/address, which on
	// most unix systems is the same as 0.0.0.0. If Tor is active, we
	// default to only listening on localhost for hidden service
	// connections.
	if len(cfg.RawListeners) == 0 {
		addr := fmt.Sprintf(":%d", defaultPeerPort)
		if cfg.Tor.Active && !cfg.Tor.DirectConnections {
			addr = fmt.Sprintf("localhost:%d", defaultPeerPort)
		}
		cfg.RawListeners = append(cfg.RawListeners, addr)
	}

	// Add default port to all RPC listener addresses if needed and remove
	// duplicate addresses.
	cfg.RPCListeners, err = lncfg.NormalizeAddresses(
		cfg.RawRPCListeners, strconv.Itoa(defaultRPCPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}

	// Add default port to all REST listener addresses if needed and remove
	// duplicate addresses.
	cfg.RESTListeners, err = lncfg.NormalizeAddresses(
		cfg.RawRESTListeners, strconv.Itoa(defaultRESTPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}

	switch {
	// The no seed backup and auto unlock are mutually exclusive.
	case cfg.NoSeedBackup && cfg.WalletUnlockPasswordFile != "":
		return nil, fmt.Errorf("cannot set noseedbackup and " +
			"wallet-unlock-password-file at the same time")

	// The "allow-create" flag cannot be set without the auto unlock file.
	case cfg.WalletUnlockAllowCreate && cfg.WalletUnlockPasswordFile == "":
		return nil, fmt.Errorf("cannot set wallet-unlock-allow-create " +
			"without wallet-unlock-password-file")

	// If a password file was specified, we need it to exist.
	case cfg.WalletUnlockPasswordFile != "" &&
		!lnrpc.FileExists(cfg.WalletUnlockPasswordFile):

		return nil, fmt.Errorf("wallet unlock password file %s does "+
			"not exist", cfg.WalletUnlockPasswordFile)
	}

	// For each of the RPC listeners (REST+gRPC), we'll ensure that users
	// have specified a safe combo for authentication. If not, we'll bail
	// out with an error. Since we don't allow disabling TLS for gRPC
	// connections we pass in tlsActive=true.
	err = lncfg.EnforceSafeAuthentication(
		cfg.RPCListeners, !cfg.NoMacaroons, true,
	)
	if err != nil {
		return nil, err
	}

	if cfg.DisableRest {
		ltndLog.Infof("REST API is disabled!")
		cfg.RESTListeners = nil
	} else {
		err = lncfg.EnforceSafeAuthentication(
			cfg.RESTListeners, !cfg.NoMacaroons, !cfg.DisableRestTLS,
		)
		if err != nil {
			return nil, err
		}
	}

	// Remove the listening addresses specified if listening is disabled.
	if cfg.DisableListen {
		ltndLog.Infof("Listening on the p2p interface is disabled!")
		cfg.Listeners = nil
		cfg.ExternalIPs = nil
	} else {

		// Add default port to all listener addresses if needed and remove
		// duplicate addresses.
		cfg.Listeners, err = lncfg.NormalizeAddresses(
			cfg.RawListeners, strconv.Itoa(defaultPeerPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}

		// Add default port to all external IP addresses if needed and remove
		// duplicate addresses.
		cfg.ExternalIPs, err = lncfg.NormalizeAddresses(
			cfg.RawExternalIPs, strconv.Itoa(defaultPeerPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}

		// For the p2p port it makes no sense to listen to an Unix socket.
		// Also, we would need to refactor the brontide listener to support
		// that.
		for _, p2pListener := range cfg.Listeners {
			if lncfg.IsUnix(p2pListener) {
				err := fmt.Errorf("unix socket addresses cannot be "+
					"used for the p2p connection listener: %s",
					p2pListener)
				return nil, err
			}
		}
	}

	// Ensure that the specified minimum backoff is below or equal to the
	// maximum backoff.
	if cfg.MinBackoff > cfg.MaxBackoff {
		return nil, fmt.Errorf("maxbackoff must be greater than minbackoff")
	}

	// Newer versions of lnd added a new sub-config for bolt-specific
	// parameters. However we want to also allow existing users to use the
	// value on the top-level config. If the outer config value is set,
	// then we'll use that directly.
	if cfg.SyncFreelist {
		cfg.DB.Bolt.SyncFreelist = cfg.SyncFreelist
	}

	// Ensure that the user hasn't chosen a remote-max-htlc value greater
	// than the protocol maximum.
	maxRemoteHtlcs := uint16(input.MaxHTLCNumber / 2)
	if cfg.DefaultRemoteMaxHtlcs > maxRemoteHtlcs {
		return nil, fmt.Errorf("default-remote-max-htlcs (%v) must be "+
			"less than %v", cfg.DefaultRemoteMaxHtlcs,
			maxRemoteHtlcs)
	}

	// Enforce only bbolt is being used in mainnet for now.
	if cfg.ActiveNetParams.Net == wire.MainNet && cfg.DB.Backend != lncfg.BoltBackend {
		return nil, fmt.Errorf("Cannot use DB backend %q in mainnet",
			cfg.DB.Backend)
	}

	// Enforce anchors are not used in mainnet.
	if cfg.ActiveNetParams.Net == wire.MainNet && !cfg.ProtocolOptions.NoAnchorCommitments() {
		return nil, fmt.Errorf("cannot use anchor commitments on mainnet")
	}

	if err := cfg.Gossip.Parse(); err != nil {
		return nil, err
	}

	// Log a warning if our expiry delta is not greater than our incoming
	// broadcast delta. We do not fail here because this value may be set
	// to zero to intentionally keep lnd's behavior unchanged from when we
	// didn't auto-cancel these invoices.
	if cfg.Invoices.HoldExpiryDelta <= lncfg.DefaultIncomingBroadcastDelta {
		ltndLog.Warnf("Invoice hold expiry delta: %v <= incoming "+
			"delta: %v, accepted hold invoices will force close "+
			"channels if they are not canceled manually",
			cfg.Invoices.HoldExpiryDelta,
			lncfg.DefaultIncomingBroadcastDelta)
	}

	// Validate the subconfigs for workers, caches, and the tower client.
	err = lncfg.Validate(
		cfg.Workers,
		cfg.Caches,
		cfg.WtClient,
		cfg.DB,
		cfg.Cluster,
		cfg.HealthChecks,
	)
	if err != nil {
		return nil, err
	}

	// Finally, ensure that the user's color is correctly formatted,
	// otherwise the server will not be able to start after the unlocking
	// the wallet.
	_, err = parseHexColor(cfg.Color)
	if err != nil {
		return nil, fmt.Errorf("unable to parse node color: %v", err)
	}

	// All good, return the sanitized result.
	return &cfg, err
}

// graphDatabaseDir returns the default directory where the local bolt graph db
// files are stored.
func (c *Config) graphDatabaseDir() string {
	return filepath.Join(
		c.DataDir, defaultGraphSubDirname,
		lncfg.NormalizeNetwork(c.ActiveNetParams.Name),
	)
}

// CleanAndExpandPath expands environment variables and leading ~ in the
// passed path, cleans the result, and returns it.
// This function is taken from https://github.com/decred/dcrd
func CleanAndExpandPath(path string) string {
	if path == "" {
		return ""
	}

	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		var homeDir string
		u, err := user.Current()
		if err == nil {
			homeDir = u.HomeDir
		} else {
			homeDir = os.Getenv("HOME")
		}

		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but the variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

func parseRPCParams(nodeConfig interface{}, net chainreg.ChainCode,
	simnet bool, flagNode string, funcName string) error {

	// First, we'll check our node config to make sure the RPC parameters
	// were set correctly. We'll also determine the path to the conf file
	// depending on the backend node.
	var daemonName, confDir, confFile string
	switch conf := nodeConfig.(type) {
	case *lncfg.DcrdConfig:
		// If both RPCUser and RPCPass are set, we assume those
		// credentials are good to use.
		if conf.RPCUser != "" && conf.RPCPass != "" {
			return nil
		}

		// Get the daemon name for displaying proper errors.
		switch net {
		case chainreg.DecredChain:
			daemonName = "dcrd"
			confFile = "dcrd"
		}

		// If only ONE of RPCUser or RPCPass is set, we assume the
		// user did that unintentionally.
		if conf.RPCUser != "" || conf.RPCPass != "" {
			return fmt.Errorf("please set both or neither of "+
				"%[1]v.rpcuser, %[1]v.rpcpass", daemonName)
		}
	}

	// If we're in simnet mode, then the running dcrd instance won't read
	// the RPC credentials from the configuration. So if lnd wasn't
	// specified the parameters, then we won't be able to start.
	if simnet {
		str := "%v: rpcuser and rpcpass must be set to your dcrd " +
			"node's RPC parameters for simnet mode"
		return fmt.Errorf(str, funcName)
	}

	fmt.Println("Attempting automatic RPC configuration to " + daemonName)

	confFile = filepath.Join(confDir, fmt.Sprintf("%v.conf", confFile))
	switch flagNode {
	case "dcrd":
		nConf := nodeConfig.(*lncfg.DcrdConfig)
		rpcUser, rpcPass, err := extractDcrdRPCParams(confFile)
		if err != nil {
			return fmt.Errorf("unable to extract RPC credentials:"+
				" %v, cannot start w/o RPC connection",
				err)
		}
		nConf.RPCUser, nConf.RPCPass = rpcUser, rpcPass
	}

	fmt.Printf("Automatically obtained %v's RPC credentials\n", daemonName)
	return nil
}

// extractDcrdRPCParams attempts to extract the RPC credentials for an existing
// dcrd instance. The passed path is expected to be the location of dcrd's
// application data directory on the target system.
func extractDcrdRPCParams(dcrdConfigPath string) (string, string, error) {
	// First, we'll open up the dcrd configuration file found at the target
	// destination.
	dcrdConfigFile, err := os.Open(dcrdConfigPath)
	if err != nil {
		return "", "", err
	}
	defer dcrdConfigFile.Close()

	// With the file open extract the contents of the configuration file so
	// we can attempt to locate the RPC credentials.
	configContents, err := ioutil.ReadAll(dcrdConfigFile)
	if err != nil {
		return "", "", err
	}

	// Attempt to locate the RPC user using a regular expression. If we
	// don't have a match for our regular expression then we'll exit with
	// an error.
	rpcUserRegexp, err := regexp.Compile(`(?m)^\s*rpcuser\s*=\s*([^\s]+)`)
	if err != nil {
		return "", "", err
	}
	userSubmatches := rpcUserRegexp.FindSubmatch(configContents)
	if userSubmatches == nil {
		return "", "", fmt.Errorf("unable to find rpcuser in config")
	}

	// Similarly, we'll use another regular expression to find the set
	// rpcpass (if any). If we can't find the pass, then we'll exit with an
	// error.
	rpcPassRegexp, err := regexp.Compile(`(?m)^\s*rpcpass\s*=\s*([^\s]+)`)
	if err != nil {
		return "", "", err
	}
	passSubmatches := rpcPassRegexp.FindSubmatch(configContents)
	if passSubmatches == nil {
		return "", "", fmt.Errorf("unable to find rpcuser in config")
	}

	return string(userSubmatches[1]), string(passSubmatches[1]), nil
}
