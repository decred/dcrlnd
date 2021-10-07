# Install

Super quick start, for knowledgeable individuals.

- Use Go >= 1.16
- Clone repo
  - `git clone https://github.com/decred/dcrlnd`
- Install:
  - `go install ./cmd/dcrlnd`
  - `go install ./cmd/dcrlncli`
- These should now work:
  - `dcrlnd --version`
  - `dcrlncli --version`

# Configuration

Create the config file ( `~/.dcrlnd/dcrlnd.conf` on linux,
`~/Library/Application Support/dcrlnd/dcrlnd.conf` on macOS,
`%LOCALAPPDATA%\dcrlnd\dcrlnd.conf` on Windows):

```
[Application Options]

node = "dcrw"
testnet = 1

[dcrwallet]

dcrwallet.spv = 1
```

# Running

Start dcrlnd: `$ dcrlnd`.

Create the wallet: `$ dcrlncli create`. Use a minimum of 8 char password. Save the seed.

# Interacting

To make it easier to interact to the node (The important bit is to always
specify `--testnet` when invoking `dcrlncli`):

```
$ alias ln=dcrlncli --testnet`
```


Get a new wallet address: `$ ln newaddress`.

Send funds to it (hint: [faucet.decred.org](https://faucet.decred.org)).

Get the balance: `$ ln walletbalance`

Connect to an online node: `$ ln connect
038fde001dbe4d6286ab168cfd1e9711ad0cbb8fc4e3c2312f2b42063b72af8e71@207.246.122.217:9735`

Open a channel: `$ ln openchannel --node_key=038fde001dbe4d6286ab168cfd1e9711ad0cbb8fc4e3c2312f2b42063b72af8e71 --local_amt=100000000 --push_amt 50000000`

Check on channel status:

```
$ ln pendingchannels
$ ln listchannels
```

Create a payment request (invoice): `$ ln addinvoice --amt=6969 --memo="Time_to_pay_the_piper!"`

Pay a payment request:

```
$ ln decodepayreq --pay_req=<PAY_REQ>
$ ln sendpayment --pay_req=<PAY_REQ>
```
