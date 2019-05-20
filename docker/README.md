# dcrlnd Docker

This document is written for people who are eager to do something with
the Decred Lightning Network Daemon (`dcrlnd`). This folder uses `docker-compose` to
package `dcrlnd` and `dcrd` together to make deploying the two daemons as easy as
typing a few commands. All configuration between `dcrlnd` and `dcrd` is handled
automatically by their `docker-compose` config file.

### Prerequisites

The code in this directory has been written and tested on Ubuntu 18.06 using the
following tools:

Name           | Version
---------------|---------
docker-compose | 1.24.0
docker         | 18.09.6
  
### Table of Contents

* [Create Lightning Network Cluster](#create-lightning-network-cluster)
* [Connect to faucet lightning node](#connect-to-faucet-lightning-node)
* [Questions](#questions)

### Create Lightning Network Cluster

This section describes a workflow on `simnet`, a development/test network
that's similar to Bitcoin Core's `regtest` mode. In `simnet` mode blocks can be
generated at will, as the difficulty is very low. This makes it an ideal
environment for testing as one doesn't need to wait tens of minutes for blocks
to arrive in order to test channel related functionality. Additionally, it's
possible to spin up an arbitrary number of `dcrlnd` instances within containers to
create a mini development cluster. All state is saved between instances using a
shared value.

Current workflow is big because we recreate the whole network by ourselves,
next versions will use the started `dcrd` Decred node in `testnet` and
`faucet` wallet from which you will get the Decred.

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

**General workflow is the following:**

* Create a `dcrd` node running on a private `simnet`.
* Create `Alice`, one of the `dcrlnd` nodes in our simulation network.
* Create `Bob`, the other `dcrlnd` node in our simulation network.
* Mine some blocks to send `Alice` some decred.
* Open channel between `Alice` and `Bob`.
* Send payment from `Alice` to `Bob`.
* Close the channel between `Alice` and `Bob`.
* Check that on-chain `Bob` balance was changed.

Start `dcrd`, and then create an address for `Alice` that we'll directly mine
decred into.

```bash
# Init decred network env variable:
$ export NETWORK="simnet"

# Run the "Alice" container and log into it:
$ docker-compose run -d --name alice lnd_dcr
$ docker exec -i -t alice bash

# Generate a new backward compatible nested p2pkh address for Alice:
alice$ dcrlncli --network=simnet newaddress p2pkh

# Recreate "dcrd" node and set Alice's address as mining address:
$ MINING_ADDRESS=SsZckVrqHRBtvhJA5UqLZ3MDXpZHi5mK6uU docker-compose up -d dcrd

# Generate 400 blocks (we need at least "100 >=" blocks because of coinbase
# block maturity and "300 ~=" in order to activate segwit):
$ docker-compose run dcrctl generate 400

# Check that segwit is active:
$ docker-compose run dcrctl getblockchaininfo | grep -A 1 segwit
```

Check `Alice` balance:

```
alice$ dcrlncli --network=simnet walletbalance
```

Connect `Bob` node to `Alice` node.

```bash
# Run "Bob" node and log into it:
$ docker-compose run -d --name bob lnd_dcr
$ docker exec -i -t bob bash

# Get the identity pubkey of "Bob" node:
bob$ dcrlncli --network=simnet getinfo

{
    ----->"identity_pubkey": "0343bc80b914aebf8e50eb0b8e445fc79b9e6e8e5e018fa8c5f85c7d429c117b38",
    "alias": "",
    "num_pending_channels": 0,
    "num_active_channels": 0,
    "num_inactive_channels": 0,
    "num_peers": 0,
    "block_height": 1215,
    "block_hash": "7d0bc86ea4151ed3b5be908ea883d2ac3073263537bcf8ca2dca4bec22e79d50",
    "synced_to_chain": true,
    "testnet": false
    "chains": [
        "decred"
    ]
}

# Get the IP address of "Bob" node:
$ docker inspect bob | grep IPAddress

# Connect "Alice" to the "Bob" node:
alice$ dcrlncli --network=simnet connect <bob_pubkey>@<bob_host>

# Check list of peers on "Alice" side:
alice$ dcrlncli --network=simnet listpeers
{
    "peers": [
        {
            "pub_key": "0343bc80b914aebf8e50eb0b8e445fc79b9e6e8e5e018fa8c5f85c7d429c117b38",
            "address": "172.19.0.4:9735",
            "bytes_sent": "357",
            "bytes_recv": "357",
            "atoms_sent": "0",
            "atoms_recv": "0",
            "inbound": true,
            "ping_time": "0"
        }
    ]
}

# Check list of peers on "Bob" side:
bob$ dcrlncli --network=simnet listpeers
{
    "peers": [
        {
            "pub_key": "03d0cd35b761f789983f3cfe82c68170cd1c3266b39220c24f7dd72ef4be0883eb",
            "address": "172.19.0.3:51932",
            "bytes_sent": "357",
            "bytes_recv": "357",
            "atoms_sent": "0",
            "atoms_recv": "0",
            "inbound": false,
            "ping_time": "0"
        }
    ]
}
```

Create the `Alice<->Bob` channel.

```bash
# Open the channel with "Bob":
alice$ dcrlncli --network=simnet openchannel --node_key=<bob_identity_pubkey> --local_amt=1000000

# Include funding transaction in block thereby opening the channel:
$ docker-compose run dcrctl generate 3

# Check that channel with "Bob" was opened:
alice$ dcrlncli --network=simnet listchannels
{
    "channels": [
        {
            "active": true,
            "remote_pubkey": "0343bc80b914aebf8e50eb0b8e445fc79b9e6e8e5e018fa8c5f85c7d429c117b38",
            "channel_point": "3511ae8a52c97d957eaf65f828504e68d0991f0276adff94c6ba91c7f6cd4275:0",
            "chan_id": "1337006139441152",
            "capacity": "1005000",
            "local_balance": "1000000",
            "remote_balance": "0",
            "commit_fee": "8688",
            "commit_size": "600",
            "fee_per_kb": "12000",
            "unsettled_balance": "0",
            "total_atoms_sent": "0",
            "total_atoms_received": "0",
            "num_updates": "0",
             "pending_htlcs": [
            ],
            "csv_delay": 4
        }
    ]
}
```

Send the payment from `Alice` to `Bob`.

```bash
# Add invoice on "Bob" side:
bob$ dcrlncli --network=simnet addinvoice --amt=10000
{
        "r_hash": "<your_random_rhash_here>",
        "pay_req": "<encoded_invoice>",
}

# Send payment from "Alice" to "Bob":
alice$ dcrlncli --network=simnet sendpayment --pay_req=<encoded_invoice>

# Check "Alice"'s channel balance
alice$ dcrlncli --network=simnet channelbalance

# Check "Bob"'s channel balance
bob$ dcrlncli --network=simnet channelbalance
```

Now we have open channel in which we sent only one payment, let's imagine
that we sent lots of them and we'd now like to close the channel. Let's do
it!

```bash
# List the "Alice" channel and retrieve "channel_point" which represents
# the opened channel:
alice$ dcrlncli --network=simnet listchannels
{
    "channels": [
        {
            "active": true,
            "remote_pubkey": "0343bc80b914aebf8e50eb0b8e445fc79b9e6e8e5e018fa8c5f85c7d429c117b38",
       ---->"channel_point": "3511ae8a52c97d957eaf65f828504e68d0991f0276adff94c6ba91c7f6cd4275:0",
            "chan_id": "1337006139441152",
            "capacity": "1005000",
            "local_balance": "990000",
            "remote_balance": "10000",
            "commit_fee": "8688",
            "commit_size": "724",
            "fee_per_kb": "12000",
            "unsettled_balance": "0",
            "total_atoms_sent": "10000",
            "total_atoms_received": "0",
            "num_updates": "2",
            "pending_htlcs": [
            ],
            "csv_delay": 4
        }
    ]
}

# Channel point consists of two numbers separated by a colon. The first one
# is "funding_txid" and the second one is "output_index":
alice$ dcrlncli --network=simnet closechannel --funding_txid=<funding_txid> --output_index=<output_index>

# Include close transaction in a block thereby closing the channel:
$ docker-compose run dcrctl generate 3

# Check "Alice" on-chain balance was credited by her settled amount in the channel:
alice$ dcrlncli --network=simnet walletbalance

# Check "Bob" on-chain balance was credited with the funds he received in the
# channel:
bob$ dcrlncli --network=simnet walletbalance
{
    "total_balance": "10000",
    "confirmed_balance": "10000",
    "unconfirmed_balance": "0"
}
```

### Connect to faucet lightning node

In order to be more confident with `dcrlnd` commands I suggest you to try
to create a mini lightning network cluster ([Create lightning network cluster](#create-lightning-network-cluster)).

In this section we will try to connect our node to the faucet/hub node
which we will create a channel with and send some amount of
decred. The schema will be following:

```
+ ----- +                   + ------ +         (1)        + --- +
| Alice | <--- channel ---> | Faucet |  <--- channel ---> | Bob |
+ ----- +                   + ------ +                    + --- +
    |                            |                           |
    |                            |                           |      <---  (2)
    + - - - -  - - - - - - - - - + - - - - - - - - - - - - - +
                                 |
                       + --------------- +
                       | Decred network  |  <---  (3)
                       + --------------- +

 (1) You may connect an additional node "Bob" and make the multihop
 payment Alice->Faucet->Bob
  
 (2) "Faucet", "Alice" and "Bob" are the lightning network daemons which
 create channels to interact with each other using the Decred network
 as source of truth.
 
 (3) In current scenario "Alice" and "Faucet" lightning network nodes
 connect to different Decred nodes. If you decide to connect "Bob"
 to "Faucet" then the already created "dcrd" node would be sufficient.
```

First of all you need to run `dcrd` node in `testnet` and wait for it to be
synced with test network (`May the Force and Patience be with you`).

```bash
# Init decred network env variable:
$ export NETWORK="testnet"

# Run "dcrd" node:
$ docker-compose up -d "dcrd"
```

After `dcrd` synced, connect `Alice` to the `Faucet` node.

A list of `Faucet` node addresses can be found at the [lightning-faucet repository](https://github.com/matheusd/lightning-faucet).

```bash
# Run "Alice" container and log into it:
$ docker-compose run -d --name alice lnd_btc; docker exec -i -t "alice" bash

# Connect "Alice" to the "Faucet" node:
alice$ dcrlncli --network=testnet connect <faucet_identity_address>@<faucet_host>
```

After a connection is achieved, the `Faucet` node should create the channel
and send some amount of decred to `Alice`.

**What you may do next?:**
- Send some amount back to `Faucet` node.
- Connect `Bob` node to the `Faucet` and make multihop payment (`Alice->Faucet->Bob`)
- Close channel with `Faucet` and check the onchain balance.

### Questions
[Decred Community](https://decred.org/community)

* How to see `alice` | `bob` | `dcrd` logs?

```bash
docker-compose logs <alice|bob|dcrd>
```
