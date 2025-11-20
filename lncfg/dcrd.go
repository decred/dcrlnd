package lncfg

type DcrdConfig struct {
	RPCHost       string `long:"rpchost" description:"The daemon's rpc listening address. If a port is omitted, then the default port for the selected chain parameters will be used."`
	RPCUser       string `long:"rpcuser" description:"Username for RPC connections"`
	RPCPass       string `long:"rpcpass" default-mask:"-" description:"Password for RPC connections"`
	RPCCert       string `long:"rpccert" description:"File containing the daemon's certificate file"`
	RawRPCCert    string `long:"rawrpccert" description:"The raw bytes of the daemon's PEM-encoded certificate chain which will be used to authenticate the RPC connection."`
	RPCClientCert string `long:"rpcclientcert" description:"TLS client certificate to present to authenticate RPC connections to dcrd"`
	RPCClientKey  string `long:"rpcclientkey" description:"Key for dcrd RPC client certificate"`
}
