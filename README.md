dcrlnd
======

[![Build Status](https://github.com/decred/dcrlnd/workflows/Build%20and%20Test/badge.svg)](https://github.com/decred/dcrlnd/actions)
[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](http://copyfree.org)
[![GoDoc](https://img.shields.io/badge/godoc-reference-blue.svg)](https://godoc.org/github.com/decred/dcrlnd)

## Lightning Network Daemon

<img src="logo.png">

The Decred Lightning Network Daemon (`dcrlnd`) - is a complete implementation of
a [Lightning Network](https://lightning.network) node and currently deployed on
`testnet3` - the Decred Test Network.

`dcrlnd` currently requires a [dcrd](https://github.com/decred/dcrd) backing
full node to perform the required chain services. The project's codebase uses
the existing set of [decred](https://github.com/decred/) libraries, and also
exports a large set of isolated re-usable Lightning Network related libraries
within it.  In the current state `dcrlnd` is capable of:
* Creating channels.
* Closing channels.
* Completely managing all channel states (including the exceptional ones!).
* Maintaining a fully authenticated+validated channel graph.
* Performing path finding within the network, passively forwarding incoming payments.
* Sending outgoing [onion-encrypted payments](https://github.com/decred/lightning-onion)
through the network.
* Updating advertised fee schedules.
* Automatic channel management ([`autopilot`](https://github.com/decred/dcrlnd/tree/master/autopilot)).

## LND Porting Status

`dcrlnd` is currently developed as a port of the original
[lnd](https://github.com/lightningnetwork/lnd) lightning network daemon with the
changes required to make it work on the Decred network and with Decred software.

Some of the most important (though by no means exhaustive) diffrences between
`lnd` and `dcrlnd` include:

- Import Paths
- Full node integration API
- Transaction serialization layout
- Transaction witness format and signature process
- Wallet integration API

## Lightning Network Specification Compliance

`dcrlnd` aims to conform to the [Lightning Network specification
(BOLTs)](https://github.com/lightningnetwork/lightning-rfc). BOLT stands for:
Basis of Lightning Technology. The specifications are currently being drafted
by several groups of implementers based around the world including the
developers of `dcrlnd`. The set of specification documents as well as our
implementation of the specification are still a work-in-progress. With that
said, the current status of `dcrlnd`'s BOLT compliance is:

  - [x] BOLT 1: Base Protocol
  - [x] BOLT 2: Peer Protocol for Channel Management
  - [x] BOLT 3: Bitcoin Transaction and Script Formats
  - [x] BOLT 4: Onion Routing Protocol
  - [x] BOLT 5: Recommendations for On-chain Transaction Handling
  - [x] BOLT 7: P2P Node and Channel Discovery
  - [x] BOLT 8: Encrypted and Authenticated Transport
  - [x] BOLT 9: Assigned Feature Flags
  - [x] BOLT 10: DNS Bootstrap and Assisted Node Location
  - [x] BOLT 11: Invoice Protocol for Lightning Payments

## Developer Resources

The daemon has been designed to be as developer friendly as possible in order
to facilitate application development on top of `dcrlnd`. Two primary RPC
interfaces are exported: an HTTP REST API, and a [gRPC](https://grpc.io/)
service. The exported API's are not yet stable, so be warned: they may change
drastically in the near future.

Most of the automatically generated documentation for the LND RPC APIs is
applicable to `dcrlnd` and can be found at
[api.lightning.community](https://api.lightning.community). The developer
resources including talks, articles, and example applications are also relevant
to `dcrlnd` and can be found at:
[dev.lightning.community](https://dev.lightning.community).

For questions and discussions, all Decred communities can be found at:

https://decred.org/community

## Installation

Knowledgeable users may use the [quickstart guide](/docs/QUICKSTART.md).

For more detailed instructions, please see [the installation
instructions](docs/INSTALL.md).

The [operation modes](docs/operation_modes.md) document lists how `dcrlnd` may
connect to the Decred network to run its operations.

And a sample config file with annotated options is [also available
here](sample-dcrlnd.conf).

## Quick Simnet Network

A shell script that uses tmux to setup a 3-node simnet network (along with
appropriate dcrd and dcrwallet nodes) is available in
[contrib/dcrlnd-tmux-3node.sh](contrib/dcrlnd-tmux-3node.sh).

Note that this requires having `dcrlnd` and `dcrlncli` in your `$PATH` variable,
as well as compatible versions of `dcrd` and `dcrwallet`.


## Docker
  To run lnd from Docker, please see the main [Docker instructions](docs/DOCKER.md)

## Safety

When operating a mainnet `dcrlnd` node, please refer to our [operational safety
guidelines](docs/safety.md). It is important to note that `dcrlnd` is still
**beta** software and that ignoring these operational guidelines can lead to
loss of funds.

## Security

Please see the [security policy](https://github.com/decred/dcrlnd/security/policy). 

## Further reading
* [Step-by-step send payment guide with docker](https://github.com/decred/dcrlnd/tree/master/docker)
* [Contribution guide](https://github.com/dcrlnd/lnd/blob/master/docs/code_contribution_guidelines.md)
