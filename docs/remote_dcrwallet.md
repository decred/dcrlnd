# Remote Dcrwallet Mode

`dcrlnd` can run in "remote dcrwallet mode", in which case instead of running an
embedded dcrwallet instance, it _connects_ to an already running wallet in order
to fetch the `dcrlnd`-specific account keys and to perform its on-chain operations.

The connection is made through dcrwallet's gRPC interface, therefore starting in
version 1.6.0, a client certificate is needed to authenticate against the
wallet instance.

The following is a quick guide on setting up such environment.

## Create the client key and certs

While this assumes two different hosts (one running the wallet and one running
the ln node) this is optional: it could be done only on a single host or all
commands could be done on the wallet host and the final files copied to the ln
node.

Note that some OSs (notably: OpenBSD and macOS) ship by default with `libressl`
instead of `openssl` and may not specifically support `ed25519` keys and
signatures used in the example, so the following commands may need adjustment
depending on the environment.

Advanced users may also tweak specific properties of the CA and client certs
according to their specific security and privacy considerations.

```shell
# Generate the CA key and cert (do it on wallet host)
$ openssl genpkey -algorithm ed25519 -out client-ca.key
$ openssl req -x509 -nodes -key client-ca.key -sha256 -days 1024 -out client-ca.cert
# (accept defaults)

# Generate the client key and CSR (do it on dcrlnd host)
$ openssl genpkey -algorithm ed25519 -out client.key
$ openssl req -new -key client.key -out client.csr
# (accept defaults)

# (copy the CSR from dcrlnd host to wallet host)

# Generate the client cert signed by the CA key.
$ openssl x509 -req -in client.csr -CA client-ca.cert -CAkey client-ca.key -CAcreateserial -out client.cert -days 1024 -sha256

# (copy client.cert and client.key to the dcrlnd host)
```

## `dcrwallet` Config

Add the following to the applicable `dcrwallet.conf` file:

```ini
[Application Options]
# Replace 127.0.0.1 for your private network IP.
# Replace 19221 for some other port (and adjust firewall).
grpclisten = 127.0.0.1:19221

# Replace for full path to client-ca.cert.
clientcafile = /path/to/client-ca.cert

```

## `dcrlnd` Config

Add the following to the applicable `dcrlnd.conf` file.

**IMPORTANT**: this should only be done _before_ setting up an embedded wallet.
You cannot switch between an embedded and a remote wallet (or between different
remote wallets initialized with different seeds).

```ini
[Application Options]

node = dcrw

# ... rest of config

[dcrwallet]

# Replace 127.0.0.1 with the private IP where the wallet is accessible.
# Replace 19221 for the correct port.
dcrwallet.grpchost = 127.0.0.1:19221

# This is a copy of the standard dcrwallet rpc.cert file.
# If you are running dcrwallet and dcrlnd on the same host
# this will be in ~/.dcrwallet/rpc.cert by default.
dcrwallet.certpath = /path/to/rpc.cert

# Account number from which the LN keys will be derived. DO NOT CHANGE after the
# dcrlnd wallet is setup. The account must already exist.
dcrwallet.accountnumber = 1

# Replace for the full path to the client.key and client.cert files previously
# created.
dcrwallet.clientkeypath = /path/to/client-ca.key
dcrwallet.clientcertpath = /path/to/client-ca.cert
```
On startup, dcrlnd will still prompt to unlock the wallet even if the dcrwallet instance is unlocked.
Execute **dcrlncli unlock**, typing the wallet's private passphrase so that dcrlnd extracts the keys needed for its operation.
