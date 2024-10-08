//go:build rpctest
// +build rpctest

package macaroons

var (
	// Below are the reduced scrypt parameters that are used when creating
	// the encryption key for the macaroon database with snacl.NewSecretKey.
	// We use very low values for our itest/rpctest to speed things up.
	scryptN = 2
	scryptR = 1
	scryptP = 1
)
