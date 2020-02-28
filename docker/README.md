# dcrlnd Docker

This document is written for people who are eager to do something with
the Decred Lightning Network Daemon (`dcrlnd`). This folder uses `docker-compose` to
package `dcrlnd`,`dcrd` and `dcrwallet`  together to make deploying the daemons as easy as
typing a few commands. All configuration between `dcrlnd`, `dcrd` and `dcrwallet` is handled
automatically by their `docker-compose` config file.

### Prerequisites

The code in this directory has been written and tested on Debian 9 using the
following tools:

Name           | Version
---------------|---------
docker-compose | 1.25.0
docker         | 19.03.5
  
### Table of Contents

* [Create Lightning Network Cluster](#create-lightning-network-cluster)
* [Questions](#questions)

## Create Lightning Network Cluster

This section describes a workflow on `simnet`, a development/test network
mode. In `simnet` mode blocks can be generated at will, 
as the difficulty is very low. This makes it an ideal
environment for testing as one doesn't need to wait tens of minutes for blocks
to arrive in order to test channel related functionality. Additionally, it's
possible to spin up an arbitrary number of `dcrlnd` instances within containers to
create a mini development cluster. All state is saved between instances using a
shared volume.


In the workflow below, we describe the steps required to recreate the following
topology, and send a payment from `Alice` to `Bob`.

```
+ ----- +                   + --- +
| Alice | <--- channel ---> | Bob |  <---  Bob and Alice are the Lightning Network daemons which
+ ----- +                   + --- +        create channels and interact with each other using the
    |                          |           Decred network as source of truth.
    |                          |
    + - - - -  - + - - - - - - +
                 |
        + -------------- +
        | Decred network |  <---  In the current scenario for simplicity we create only one
        + -------------- +        "dcrd" node which represents the Decred network, in a
                                  real situation Alice and Bob will likely be
                                  connected to different Decred nodes.
```

Run `docker-compose up --no-start` to create network, volumes and build the services `dcrd`, `dcrwallet`, `dcrctl` and `dcrlnd`.

### dcrd, dcrwallet and MVW
We need to start `dcrd` service and generate 18 blocks for the first coinbase to mature, because without coins, the MVW(Minimum Voting Wallet) can't buy tickets.
```
# Run dcrd service for the first time
docker-compose start dcrd

# Use dcrctl to generate 18 blocks
docker-compose run dcrctl generate 18
```
Now we can start the `dcrwallet` and the MVW will work great.
`docker-compose start dcrwallet`

### dcrlnd, add Alice and Bob's nodes

Create `Alice`s container and send funds from `dcrwallet`:
```bash
#Create "Alice"s container
docker-compose run -d --name alice dcrlnd

#Log into the "Alice" container
docker exec -it alice bash

# Generate a new p2pkh address for Alice:
alice$ dcrlncli --simnet newaddress
```
We can keep logged in `Alice` container, just need to use another terminal tab to execute commands in docker-compose.

 * Send 1DCR from `dcrwallet` to `Alice` address 
```bash
#Send from MVW to Alice's LNWallet
docker-compose run dcrctl --wallet sendfrom default <alice_address> 1

#Generate a block to update the wallet balance
docker-compose run dcrctl generate 1

#Check Alice's wallet balance
alice$ dcrlncli --simnet walletbalance
```

Create `Bob`s container and connect to `Alice`:
```bash
#Create "Bob"s container
docker-compose run -d --name bob dcrlnd

#Log into the "Bob" container
docker exec -it bob bash
```

Connect Bob node to Alice node
```bash
# Get identity pubkey from Bob's node
bob$ dcrlncli --simnet getinfo
{
	"version": "0.2.0-pre+dev",
	------>"identity_pubkey": "02fe9daea36c1b1bd9e8dc1f5dc9edfc887d223199f2bac99e1cf92eb8cdb654e7",
	"alias": "02fe9daea36c1b1bd9e8",
	"color": "#3399ff",
	"num_pending_channels": 0,
	"num_active_channels": 0,
	"num_inactive_channels": 0,
	"num_peers": 0,
	"block_height": 101,
	"block_hash": "0000066c30cee9bae93a05b9b0edacef1fea1443f4b43a96f5d0761a70965c18",
	"best_header_timestamp": 1576527447,
	"synced_to_chain": true,
	"synced_to_graph": false,
	"testnet": false,
	"chains": [
		{
			"chain": "decred",
			"network": "simnet"
		}
	],
	"uris": null
}

# Connect Alice to Bob's node
alice$ dcrlncli --simnet connect <bob_pubkey>@bob

# Check list of peers on "Alice" side:
alice$ dcrlncli --simnet listpeers
{
    "peers": [
        {
            "pub_key": "02fe9daea36c1b1bd9e8dc1f5dc9edfc887d223199f2bac99e1cf92eb8cdb654e7",
            "address": "172.25.0.5:9735",
            "bytes_sent": "139",
            "bytes_recv": "139",
            "atoms_sent": "0",
            "atoms_recv": "0",
            "inbound": false,
            "ping_time": "0",
            "sync_type": "ACTIVE_SYNC"
        }
    ]
}
```

Create the `Alice<->Bob` channel.
```bash
# Open the channel with "Bob":
alice$ dcrlncli --simnet openchannel --node_key=<bob_pubkey> --local_amt=100000

# Include funding transaction in block thereby opening the channel:
# We need six confirmations to channel active
$ docker-compose run dcrctl generate 6

# Check that channel with "Bob" was opened:
alice$ dcrlncli --simnet listchannels
{
    "channels": [
        {
            "active": true,
            "remote_pubkey": "024d7bcd6b817955cbca60a2b2dff15f9615eb50f6ad16db45578722505d5817b1",
            "channel_point": "16191b078727755138d892b7cfa08588762a7d52469710fd45d1758594dae4b5:0",
            "chan_id": "42880953745408",
            "capacity": "100000",
            "local_balance": "96360",
            "remote_balance": "0",
            "commit_fee": "3640",
            "commit_size": "328",
            "fee_per_kb": "10000",
            "unsettled_balance": "0",
            "total_atoms_sent": "0",
            "total_atoms_received": "0",
            "num_updates": "0",
            "pending_htlcs": [
            ],
            "csv_delay": 288,
            "private": false,
            "initiator": true,
            "chan_status_flags": "ChanStatusDefault",
            "local_chan_reserve_atoms": "6030",
            "remote_chan_reserve_atoms": "6030",
            "static_remote_key": true
        }
    ]
}

```

Send the payment from Alice to Bob.
```bash
# Add invoice on "Bob" side:
bob$ dcrlncli --simnet addinvoice --amt=50000
{
        "r_hash": "<your_random_rhash_here>", 
        "pay_req": "<encoded_invoice>", 
        "add_index": 1
}

# Send payment from "Alice" to "Bob":
alice$ dcrlncli --simnet payinvoice <encoded_invoice>

# Check "Alice"'s channel balance
alice$ dcrlncli --simnet channelbalance

```

Now we have open channel in which we sent only one payment, let's imagine that we sent lots of them and we'd now like to close the channel. Let's do it!

```bash
# List the "Alice" channel and retrieve "channel_point" which represents
# the opened channel:
alice$ dcrlncli --simnet listchannels
{
    "channels": [
        {
            "active": true,
            "remote_pubkey": "024d7bcd6b817955cbca60a2b2dff15f9615eb50f6ad16db45578722505d5817b1",
            ------>"channel_point": "16191b078727755138d892b7cfa08588762a7d52469710fd45d1758594dae4b5:0",
            "chan_id": "42880953745408",
            "capacity": "100000",
            "local_balance": "46360",
            "remote_balance": "50000",
            "commit_fee": "3640",
            "commit_size": "364",
            "fee_per_kb": "10000",
            "unsettled_balance": "0",
            "total_atoms_sent": "50000",
            "total_atoms_received": "0",
            "num_updates": "2",
            "pending_htlcs": [
            ],
            "csv_delay": 288,
            "private": false,
            "initiator": true,
            "chan_status_flags": "ChanStatusDefault",
            "local_chan_reserve_atoms": "6030",
            "remote_chan_reserve_atoms": "6030",
            "static_remote_key": true
        }
    ]
}

# Channel point consists of two numbers separated by a colon. The first one 
# is "funding_txid" and the second one is "output_index":
alice$ dcrlncli --simnet closechannel <funding_txid> <output_index>

# Include close transaction in a block thereby closing the channel:
$ docker-compose run dcrctl generate 6

# Check "Alice" on-chain balance was credited by her settled amount in the channel:
alice$ dcrlncli --simnet walletbalance

# Check "Bob" on-chain balance was credited with the funds he received in the
# channel:
bob$ dcrlncli --simnet walletbalance
{
    "total_balance": "50000",
    "confirmed_balance": "50000",
    "unconfirmed_balance": "0"
}
```

## Connect through the Bob Node

We don't need to connect with all nodes for make a payment, looks this case:

```bash
+ ----- +                     + --- +         (1)        + ----- +
| Alice |  <--- channel --->  | Bob |  <--- channel ---> | Carol |
+ ----- +                     + --- +                    + ----- +
    |                            |                           |
    |                            |                           |      <---  (2)
    + - - - -  - - - - - - - - - + - - - - - - - - - - - - - +
                                 |
                         + -------------- +
                         | Decred network |
                         + -------------- +


 (1) You may connect an additional node "Carol" and make the multihop
 payment Alice->Bob->Carol

 (2) "Alice", "Bob" and "Carol" are the lightning network daemons which
 create channels to interact with each other using the Decred network
 as source of truth.

```

Create the `Alice<->Bob` channel.
```bash
# Open the channel with "Bob":
alice$ dcrlncli --simnet openchannel --node_key=<bob_pubkey> --local_amt=100000

# Include funding transaction in block thereby opening the channel:
# We need six confirmations to channel active
$ docker-compose run dcrctl generate 6
```

Create the Carol's node and get pubkey

```bash
# Create "Carol"s container
docker-compose run -d --name carol dcrlnd

# Log into the "Carol" container
docker exec -it carol bash

# Get identity pubkey from Carol's node
carol$ dcrlncli --simnet getinfo

```

Bob now can create a channel with Carol

```bash
# Connect Bob to Carol's node
bob$ dcrlncli --simnet connect <carol_pubkey>@carol

# Open the channel with "Carol":
bob$ dcrlncli --simnet openchannel --node_key=<carol_pubkey> --local_amt=40000

# Include funding transaction in block thereby opening the channel:
docker-compose run dcrctl generate 6

# Check that channel with "Carol" was opened:
bob$ dcrlncli --simnet listchannels

```

This is what we have now:

```bash
  96360                        0
+ ----- +                   + --- +                  + ----- +
| Alice |  <--- total --->  | Bob |  <--- total ---> | Carol |
+ ----- +      100000       + --- +       40000      + ----- +
                             36360                       0
```

We will create an invoice on Carol's node to Alice pay her,
this is a multihop payment, because Alice don't know Carol and the payment will pass thougth Bob.

```bash
# Generate an invoice on carol's node
carol$ dcrlncli --simnet addinvoice --amt 1000

# Pay this invoice with Alice's LNWallet
alice$ dcrlncli --simnet payinvoice <carol_invoice>
```

Look to the final channels balance:
```bash
  95358                      1001
+ ----- +                   + --- +                  + ----- +
| Alice |  <--- total --->  | Bob |  <--- total ---> | Carol |
+ ----- +      100000       + --- +       40000      + ----- +
                              35360                    1000
```

## Questions
[Decred Community](https://decred.org/community)

* How to see `alice` | `bob` | `carol` logs?

```bash
docker logs <alice|bob|carol>
```
