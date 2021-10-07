# dcrlnd Operation Modes

`dcrlnd` supports multiple operation modes. The choices are:

- Chain Operations
  - through dcrd
  - through the underlying wallet
- On-Chain Wallet Operations
  - using embedded wallet
  - using remote wallet


The following table lists the trade-offs for each combination:

| Combination | Characteristcs |
| --- | --- |
| dcrd + embedded wallet | - Safest for on-chain ops <br> - Fastest on-chain sync <br> - 2 daemons to maintain  <br> - Best for standalone hubs |
| dcrd + remote wallet | - Safest for on-chain ops <br> - No additional seeds to manage <br> - 3 daemons to maintain <br> - Hardest to setup <br> - Useful when user already maintains hot wallet |
| spv + embedded wallet | - Simplest to setup <br> - Slower on-chain sync <br> - 1 daemon to maintain |
| spv + remote wallet | - No additional seeds to manange <br> - 2 daemons to maintain <br> - Useful for integrating into existing setups (e.g. Decrediton) |

The following sections detail what the choices imply. After that, sample config
files are provided for the different combinations.

## Chain Operations: dcrd vs wallet

`dcrlnd` requires access to the Decred P2P network in order to perform its
on-chain duties (open and close channels, detect force closures, send and
receive on-chain funds, etc).

Using an underlying `dcrd` instance is the safest option here, since it ensures
`dcrlnd` has access to a completely valid chain and mempool.

It's also the fastest option, given that the the wallet syncs faster when
running in RPC mode (either the embedded one or a remote wallet).

The drawback of using a `dcrd` instance is having to also maintain it as an
online service and to ensure `dcrlnd` always has access to it.

When operating an always-on `dcrlnd` hub node, it's the recommended chain sync
option.

The advantage of using the wallet itself (possibly in SPV mode) for chain 
operations is that it simplifies the setup a bit and removes the need to
maintain another service online.

## Wallet Operations: embedded vs remote

`dcrlnd` also requies access to a `dcrwallet` instance in order to perform its
on-chain wallet operations (signing transactions, funding and withdrawing funds
from channels, etc).

When running in embedded wallet mode, `dcrlnd` creates its own internal instance
of `dcrwallet` and manages its operation directly. This simplifies the
deployment by avoiding the need to run and maintain a separate `dcrwallet`
service running, but has the drawback that the user needs to maintain an
additional seed: the seed to the on-chain side of `dcrlnd`'s operations.

Using a remote wallet is useful when the user already maintains an online, hot
wallet due to other reasons (for example due to continuously running
stakeshuffle sessions, solo voting or something else). This is also useful for
integrating to other applications (such as Decrediton) where the user already
has a wallet.

Note that it's **strongly** recommended to use a **separate**, **dedicated**
account to LN operations when connecting to an already running wallet to avoid
`dcrlnd` operations interfering with standard wallet usage (and vice-versa).


## Sample Configs

The following are the absolute minimum configuration necessary to run each
combination. Additional config (such as `testnet = 1`) might be necessary
according to the specific use case.

### dcrd + Embedded Wallet

```ini
[Application Options]

node = dcrd

[Dcrd]

dcrd.rpchost = localhost
dcrd.rpcuser = USER
dcrd.rpcpass = PASSWORD
dcrd.rpccert = ~/.dcrd/rpc.cert
```

### spv + Embedded Wallet

```ini
[Application Options]

node = "dcrw"

[dcrwallet]

dcrwallet.spv = 1

```


### Remote Wallet

This is applicable to a wallet that runs both in RPC and SPV mode, since it's
the wallet that is going to determine how the sync happens. 

Chain operations always happen through the wallet. See the [Remote Dcrwallet
Mode](./remote_dcrwallet.md) doc for additional info on setting up this mode of
operation.

```ini
[Application Options]

node = dcrw

[dcrwallet]

dcrwallet.grpchost = 127.0.0.1:19221
dcrwallet.certpath = ~/.dcrwallet/rpc.cert 
dcrwallet.accountnumber = 1
dcrwallet.clientkeypath = /path/to/client-ca.key
dcrwallet.clientcertpath = /path/to/client-ca.cert

```
