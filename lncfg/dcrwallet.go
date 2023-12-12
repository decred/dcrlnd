package lncfg

import "decred.org/dcrwallet/v4/p2p"

type DcrwalletConfig struct {
	GRPCHost       string `long:"grpchost" description:"The wallet's grpc listening address. If a port is omitted, then the default port for the selected chain parameters will be used."`
	CertPath       string `long:"certpath" description:"The file containing the wallet's certificate file."`
	AccountNumber  int32  `long:"accountnumber" description:"The account number that dcrlnd should take control of for all onchain operations and offchain key derivation."`
	ClientKeyPath  string `long:"clientkeypath" description:"The file containing a client private key to use when connecting to a remote wallet"`
	ClientCertPath string `long:"clientcertpath" description:"The file containing the client certificate to use when connecting to a remote wallet"`

	SPV        bool     `long:"spv" description:"Whether to use SPV mode when using an embedded wallet"`
	SPVConnect []string `long:"spvconnect" description:"Addresses to connect to when using spv mode"`

	DialFunc p2p.DialFunc
}
