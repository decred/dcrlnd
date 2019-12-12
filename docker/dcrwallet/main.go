package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"

	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var certificateFile = "/rpc/rpc.cert"

func main() {
	// Load credentials
	creds, err := credentials.NewClientTLSFromFile(certificateFile, "localhost")
	if err != nil {
		fmt.Println(err)
		return
	}

	// Create te grpc connection
	conn, err := grpc.Dial("localhost:19558", grpc.WithTransportCredentials(creds))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()

	// Init the loader service client used for
	// create a new wallet
	lc := pb.NewWalletLoaderServiceClient(conn)

	createWalletRequest := &pb.CreateWalletRequest{
		PrivatePassphrase: []byte(os.Getenv("WALLET_PASS")),
		Seed:              []byte(os.Getenv("WALLET_SEED")),
	}

	// Create/import a wallet
	_, err = lc.CreateWallet(context.Background(), createWalletRequest)
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

	nextAddressResponse, err := c.NextAddress(context.Background(), nextAddressRequest)
	if err != nil {
		fmt.Println(err)
		return
	}

	miningAddress := nextAddressResponse.GetAddress()
	fmt.Printf("\033[1;34mNew address generated: %v\n\033[0m", miningAddress)

	// Create the dcrd config file with new mining address
	data := []byte(fmt.Sprintf("miningaddr=%v", miningAddress))
	err = ioutil.WriteFile("/data/dcrd.conf", data, 0644)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("\033[1;35mdcrd.conf created!\033[0m")

	return
}
