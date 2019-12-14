# Table of Contents
* [Installation](#installation)
    * [Preliminaries](#preliminaries)
    * [Installing dcrlnd](#installing-dcrlnd)
* [Available Backend Operating Modes](#available-backend-operating-modes)
  * [dcrd Options](#dcrd-options)
  * [Using dcrd](#using-dcrd)
    * [Installing dcrd](#installing-dcrd)
    * [Starting dcrd](#starting-dcrd)
    * [Running dcrlnd using the dcrd backend](#running-dcrlnd-using-the-dcrd-backend)
* [Macaroons](#macaroons)
* [Network Reachability](#network-reachability)
* [Simnet vs. Testnet Development](#simnet-vs-testnet-development)
* [Creating an dcrlnd.conf (Optional)](#creating-an-dcrlndconf-optional)

# Installation

### Preliminaries
  In order to work with [`dcrlnd`](https://github.com/decred/dcrlnd),
  the following build dependencies are required:

  * **Go:** `dcrlnd` is written in Go. To install, run one of the following commands:


    **Note**: The minimum version of Go supported is Go 1.13. We recommend that
    users use the latest version of Go, which at the time of writing is
    [`1.13`](https://blog.golang.org/go1.13).


    On Linux:
    ```
    sudo apt-get install golang-1.13-go
    ```
    > Note that golang-1.13-go puts binaries in /usr/lib/go-1.13/bin. If you want them on your PATH, you need to make that change yourself. Alternatively, you can run:
    ```
    sudo ln -s /usr/lib/go-1.13/bin/go /usr/local/bin/go
    ```

    On Mac OS X:
    ```
    brew install go
    ```

    On FreeBSD:
    ```
    pkg install go
    ```

    Alternatively, one can download the pre-compiled binaries hosted on the
    [golang download page](https://golang.org/dl/). If one seeks to install
    from source, then more detailed installation instructions can be found
    [here](http://golang.org/doc/install).

    At this point, you should set your `$GOPATH` environment variable, which
    represents the path to your workspace. By default, `$GOPATH` is set to
    `~/go`. You will also need to add `$GOPATH/bin` to your `PATH`. This ensures
    that your shell will be able to detect the binaries you install.

    ```bash
    export GOPATH=~/gocode
    export PATH=$PATH:$GOPATH/bin
    ```

    We recommend placing the above in your .bashrc or in a setup script so that
    you can avoid typing this every time you open a new terminal window.

  * **go modules:** This project uses [go modules](https://github.com/golang/go/wiki/Modules) 
    to manage dependencies as well as to provide *reproducible builds*.

    Usage of go modules (with go 1.13) means that you no longer need to clone
    `dcrlnd` into your `$GOPATH` for development purposes. Instead, your
    `dcrlnd` repo can now live anywhere!

### Installing dcrlnd

With the preliminary steps completed, to install `dcrlnd`, `dcrlncli`, and all
related dependencies run the following commands:
```
go get -d github.com/decred/dcrlnd
cd $GOPATH/src/github.com/decred/dcrlnd
make && make install
```

**NOTE**: Our instructions still use the `$GOPATH` directory from prior
versions of Go, but with go 1.11, it's now possible for `dcrlnd` to live
_anywhere_ on your file system.

For Windows WSL users, make will need to be referenced directly via
/usr/bin/make/, or alternatively by wrapping quotation marks around make,
like so:

```
/usr/bin/make && /usr/bin/make install

"make" && "make" install
```

On FreeBSD, use gmake instead of make.

Alternatively, if one doesn't wish to use `make`, then the `go` commands can be
used directly:
```
GO111MODULE=on go install -v ./...
```

**Updating**

To update your version of `dcrlnd` to the latest version run the following
commands:
```
cd $GOPATH/src/github.com/decred/dcrlnd
git pull
make clean && make && make install
```

On FreeBSD, use gmake instead of make.

Alternatively, if one doesn't wish to use `make`, then the `go` commands can be
used directly:
```
cd $GOPATH/src/github.com/decred/dcrlnd
git pull
GO111MODULE=on go install -v ./...
```

**Tests**

To check that `dcrlnd` was installed properly run the following command:
```
make check
```

# Available Backend Operating Modes

In order to run, `dcrlnd` requires, that the user specify a chain backend. At
the time of writing of this document, only the `dcrd` backend can be used. We
currently *require* `--txindex` when running with `dcrd`.


The set of arguments for each of the backend modes is as follows:

## dcrd Options
```
dcrd:
      --dcrd.dir=                                             The base directory that contains the node's data, logs, configuration file, etc. (default: /Users/roasbeef/Library/Application Support/Dcrd)
      --dcrd.rpchost=                                         The daemon's rpc listening address. If a port is omitted, then the default port for the selected chain parameters will be used. (default: localhost)
      --dcrd.rpcuser=                                         Username for RPC connections
      --dcrd.rpcpass=                                         Password for RPC connections
      --dcrd.rpccert=                                         File containing the daemon's certificate file (default: /Users/roasbeef/Library/Application Support/Dcrd/rpc.cert)
      --dcrd.rawrpccert=                                      The raw bytes of the daemon's PEM-encoded certificate chain which will be used to authenticate the RPC connection.
```

## Using dcrd

### Installing dcrd

On FreeBSD, use gmake instead of make.

To install dcrd, run the following commands:

Install **dcrd**:
```
make dcrd
```

Alternatively, you can install [`dcrd` directly from its
repo](https://github.com/decred/dcrd).

### Starting dcrd

Running the following command will create `rpc.cert` and default `dcrd.conf`.

```
dcrd --testnet --rpcuser=REPLACEME --rpcpass=REPLACEME --txindex
```
If you want to use `dcrlnd` on testnet, `dcrd` needs to first fully sync the
testnet blockchain. Depending on your hardware, this may take up to a few hours.

While `dcrd` is syncing you can check on its progress using dcrd's `getinfo`
RPC command:
```
dcrctl --testnet --rpcuser=REPLACEME --rpcpass=REPLACEME getinfo
{
  "version": 120000,
  "protocolversion": 70002,
  "blocks": 1114996,
  "timeoffset": 0,
  "connections": 7,
  "proxy": "",
  "difficulty": 422570.58270815,
  "testnet": true,
  "relayfee": 0.00001,
  "errors": ""
}
```

Additionally, you can monitor dcrd's logs to track its syncing progress in real
time.

You can test your `dcrd` node's connectivity using the `getpeerinfo` command:
```
dcrctl --testnet --rpcuser=REPLACEME --rpcpass=REPLACEME getpeerinfo | more
```

### Running dcrlnd using the dcrd backend

If you are on testnet, run this command after `dcrd` has finished syncing.
Otherwise, replace `--testnet` with `--simnet`. If you are
installing `dcrlnd` in preparation for the
[tutorial](http://dev.lightning.community/tutorial), you may skip this step.
```
dcrlnd --testnet --debuglevel=debug --dcrd.rpcuser=kek --dcrd.rpcpass=kek --externalip=X.X.X.X
```

# Macaroons

`dcrlnd`'s authentication system is called **macaroons**, which are
decentralized bearer credentials allowing for delegation, attenuation, and other
cool features. You can learn more about them in Alex Akselrod's [writeup on
 the original lnd issue](https://github.com/lightningnetwork/lnd/issues/20).

Running `dcrlnd` for the first time will by default generate the
`admin.macaroon`, `read_only.macaroon`, and `macaroons.db` files that are used
to authenticate into `dcrlnd`. They will be stored in the network directory
(default: `dcrlnddir/data/chain/decred/mainnet`) so that it's possible to use a
distinct password for mainnet, testnet, simnet, etc. Note that if you specified
an alternative data directory (via the `--datadir` argument), you will have to
additionally pass the updated location of the `admin.macaroon` file into
`dcrlncli` using the `--macaroonpath` argument.

To disable macaroons for testing, pass the `--no-macaroons` flag into *both*
`dcrlnd` and `dcrlncli`.

# Network Reachability

If you'd like to signal to other nodes on the network that you'll accept
incoming channels (as peers need to connect inbound to initiate a channel
funding workflow), then the `--externalip` flag should be set to your publicly
reachable IP address.

# Simnet vs. Testnet Development

If you are doing local development, such as for the tutorial, you'll want to
start both `dcrd` and `dcrlnd` in the `simnet` mode. Simnet is similar to
regtest in that you'll be able to instantly mine blocks as needed to test
`dcrlnd` locally. In order to start either daemon in the `simnet` mode use
`simnet` instead of `testnet`, adding the `--simnet` flag instead of the
`--testnet` flag.

Another relevant command line flag for local testing of new `dcrlnd`
developments is the `--debughtlc` flag. When starting `dcrlnd` with this flag,
it'll be able to automatically settle a special type of HTLC sent to it. This
means that you won't need to manually insert invoices in order to test payment
connectivity. To send this "special" HTLC type, include the `--debugsend`
command at the end of your `sendpayment` commands.


# Creating a dcrlnd.conf (Optional)

Optionally, if you'd like to have a persistent configuration between `dcrlnd`
launches, allowing you to simply type `dcrlnd --testnet`
at the command line, you can create an `dcrlnd.conf`.

**On MacOS, located at:**
`/Users/[username]/Library/Application Support/dcrlnd/dcrlnd.conf`

**On Linux, located at:**
`~/.dcrlnd/dcrlnd.conf`

Here's a sample `dcrlnd.conf` for `dcrd` to get you started:
```
[Application Options]
debuglevel=trace
maxpendingchannels=10

[Decred]
testnet=1
```

Notice the `[Decred]` section. This section houses the parameters for the
Decred chain. See a more detailed sample config file available
[here](https://github.com/decred/dcrlnd/blob/master/sample-dcrlnd.conf)
and explore the other sections for node configuration.
