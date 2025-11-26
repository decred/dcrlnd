package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	pb "decred.org/dcrwallet/v5/rpc/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	dcrdConfigFile  = "/data/dcrd.conf"
	certificateFile = "/rpc/rpc.cert"
	keyFile         = "/rpc/rpc.key"
)

func tlsCertFromFile(fname string) (*x509.CertPool, error) {
	b, err := os.ReadFile(fname)
	if err != nil {
		return nil, err

	}
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("credentials: failed to append certificates")

	}

	return cp, nil
}

func main() {
	// Load credentials
	caCert, err := tlsCertFromFile(certificateFile)
	if err != nil {
		fmt.Println(err)
		return
	}
	clientCert, err := tls.LoadX509KeyPair(certificateFile, keyFile)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Setup the TLS config.
	tlsCfg := &tls.Config{
		ServerName:   "localhost",
		RootCAs:      caCert,
		Certificates: []tls.Certificate{clientCert},
	}
	creds := credentials.NewTLS(tlsCfg)

	// Create te grpc connection
	conn, err := grpc.Dial("localhost:19558", grpc.WithTransportCredentials(creds))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()

	// Init the loader service client used for
	// create a new wallet
	ctx := context.Background()
	lc := pb.NewWalletLoaderServiceClient(conn)

	createWalletRequest := &pb.CreateWalletRequest{
		PrivatePassphrase: []byte(os.Getenv("WALLET_PASS")),
		Seed:              []byte(os.Getenv("WALLET_SEED")),
	}

	// Create/import a wallet
	_, err = lc.CreateWallet(ctx, createWalletRequest)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("\033[1;35mWallet created!\033[0m")

	// Init the wallet service client to request
	// an new address for past wallet imported.
	c := pb.NewWalletServiceClient(conn)

	nextAddressRequest := &pb.NextAddressRequest{
		Account: 0,
		Kind:    pb.NextAddressRequest_BIP0044_EXTERNAL,
	}

	nextAddressResponse, err := c.NextAddress(ctx, nextAddressRequest)
	if err != nil {
		fmt.Println(err)
		return
	}

	miningAddress := nextAddressResponse.GetAddress()
	fmt.Printf("\033[1;34mNew address generated: %v\n\033[0m", miningAddress)

	// Create the dcrd config file with new mining address
	data := []byte(fmt.Sprintf("miningaddr=%v", miningAddress))
	err = os.WriteFile(dcrdConfigFile, data, 0644)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("\033[1;35mdcrd.conf created!\033[0m")

	return
}
