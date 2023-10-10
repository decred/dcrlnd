package walletunlocker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"

	pb "decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrlnd/lnrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func tlsCertFromFile(fname string) (*x509.CertPool, error) {
	b, err := ioutil.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("credentials: failed to append certificates")
	}

	return cp, nil
}

// UnlockRemoteWallet sends the password provided by the incoming
// UnlockRemoteWalletRequest over the UnlockMsgs channel in case it
// successfully decrypts an existing remote wallet.
func (u *UnlockerService) unlockRemoteWallet(ctx context.Context,
	in *lnrpc.UnlockWalletRequest) (*lnrpc.UnlockWalletResponse, error) {

	db := u.db.Load()
	if db == nil {
		return nil, fmt.Errorf("attempting to unlock before unlocker service has DB")
	}

	// dcrwallet rpc.cert.
	caCert, err := tlsCertFromFile(u.dcrwCert)
	if err != nil {
		return nil, err
	}

	// Figure out the client cert.
	var clientCerts []tls.Certificate
	if in.DcrwClientKeyCert != nil {
		// Received a key+cert blob from dcrwallet ipc. X509KeyPair is
		// smart enough to decode the correct element from the
		// concatenated blob, so it's ok to pass the same byte slice in
		// both arguments.
		cert, err := tls.X509KeyPair(in.DcrwClientKeyCert, in.DcrwClientKeyCert)
		if err != nil {
			return nil, err
		}
		clientCerts = []tls.Certificate{cert}
	} else if u.dcrwClientKey != "" && u.dcrwClientCert != "" {
		cert, err := tls.LoadX509KeyPair(u.dcrwClientCert, u.dcrwClientKey)
		if err != nil {
			return nil, err
		}
		clientCerts = []tls.Certificate{cert}
	}

	tlsCfg := &tls.Config{
		ServerName:   "localhost",
		RootCAs:      caCert,
		Certificates: clientCerts,
	}
	creds := credentials.NewTLS(tlsCfg)

	// Connect to the wallet.
	conn, err := grpc.Dial(u.dcrwHost, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}

	wallet := pb.NewWalletServiceClient(conn)

	// Unlock the account.
	unlockAcctReq := &pb.UnlockAccountRequest{
		AccountNumber: uint32(u.dcrwAccount),
		Passphrase:    in.WalletPassword,
	}
	_, err = wallet.UnlockAccount(ctx, unlockAcctReq)
	if err != nil {
		return nil, fmt.Errorf("unable to unlock account: %v", err)
	}

	// Ensure we can grab the privkey for the given account.
	getAcctReq := &pb.GetAccountExtendedPrivKeyRequest{
		AccountNumber: uint32(u.dcrwAccount),
	}
	getAcctResp, err := wallet.GetAccountExtendedPrivKey(ctx, getAcctReq)

	// Irrespective of the return of GetAccountExtendedPrivKey, re-lock the
	// account.
	lockAcctReq := &pb.LockAccountRequest{
		AccountNumber: uint32(u.dcrwAccount),
	}
	_, lockErr := wallet.LockAccount(ctx, lockAcctReq)
	if lockErr != nil {
		log.Errorf("Error while locking account number %d: %v",
			u.dcrwAccount, lockErr)
	}

	// And now check if GetAccountExtendedPrivKey returned an error.
	if err != nil {
		return nil, fmt.Errorf("unable to get xpriv: %v", err)
	}

	// Ensure we don't attempt to use a keyring derived from a different
	// account than previously used by comparing the first external public
	// key with the one stored in the database.
	acctXPriv, err := hdkeychain.NewKeyFromString(
		getAcctResp.AccExtendedPrivKey, u.netParams,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create account xpriv: %v", err)
	}

	branchExtXPriv, err := acctXPriv.Child(0)
	if err != nil {
		return nil, fmt.Errorf("unable to derive the external branch xpriv: %v", err)
	}

	firstKey, err := branchExtXPriv.Child(0)
	if err != nil {
		return nil, fmt.Errorf("unable to derive first external key: %v", err)
	}
	firstPubKeyBytes := firstKey.SerializedPubKey()
	if err = db.CompareAndStoreAccountID(firstPubKeyBytes); err != nil {
		return nil, fmt.Errorf("account number %d failed to generate "+
			"previously stored account ID: %v", u.dcrwAccount, err)
	}

	// We successfully opened the wallet and pass the instance back to
	// avoid it needing to be unlocked again.
	walletUnlockMsg := &WalletUnlockMsg{
		Passphrase:    in.WalletPassword,
		Conn:          conn,
		UnloadWallet:  func() error { return nil },
		StatelessInit: in.StatelessInit,
	}

	// Before we return the unlock payload, we'll check if we can extract
	// any channel backups to pass up to the higher level sub-system.
	chansToRestore := extractChanBackups(in.ChannelBackups)
	if chansToRestore != nil {
		walletUnlockMsg.ChanBackups = *chansToRestore
	}

	// At this point we were able to open the existing wallet with the
	// provided password. We send the password over the UnlockMsgs channel,
	// such that it can be used by lnd to open the wallet.
	select {
	case u.UnlockMsgs <- walletUnlockMsg:
		// We need to read from the channel to let the daemon continue
		// its work. But we don't need the returned macaroon for this
		// operation, so we read it but then discard it.
		select {
		case adminMac := <-u.MacResponseChan:
			return &lnrpc.UnlockWalletResponse{
				AdminMacaroon: adminMac,
			}, nil

		case <-ctx.Done():
			return nil, ErrUnlockTimeout
		}

	case <-ctx.Done():
		return nil, ErrUnlockTimeout
	}
}
