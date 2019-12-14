# Install

Super quick start, for knowledgeable individuals.

- Use Go >= 1.11
- Git clone as usual
- `make install`
- These should now work:
  - `dcrlnd --version`
  - `dcrlncli --version`

# Configuration

Create the config file ( `~/.dcrlnd/dcrlnd.conf` on linux,
`~/Library/Application Support/dcrlnd/dcrlnd.conf` on macOS,
`%LOCALAPPDATA%\dcrlnd\dcrlnd.conf` on Windows):

```
[Application Options]

debuglevel = debug
# alias = "add a descriptive name and uncomment"

[Decred]
node = "dcrd"
testnet = 1

[dcrd]
dcrd.rpchost = localhost:19109
dcrd.rpcuser = USER
dcrd.rpcpass = PASSWORD
dcrd.rpccert = /home/user/.dcrd/rpc.cert
```

Modify as needed.

# Running

Start dcrlnd: `$ dcrlnd`.

Create the wallet: `$ dcrlncli create`. Use a minimum of 8 char password. Save the seed.

# Interacting

To make it easier: `$ alias ln=dcrlncli -n testnet` (the important bit is to always specify `-n testnet` when invoking `dcrlncli`).

Get a new wallet address: `$ ln newaddress p2pkh`.

Send funds to it (hint: [faucet.decred.org](https://faucet.decred.org)).

Get the balance: `$ ln walletbalance`

Connect to an online node: `$ ln connect 0374ee2dec7de3732c42b4f8229c001c4297b4858fac27069b4bdba854348916a8@207.246.122.217`

Open a channel: `$ ln openchannel --node_key=0374ee2dec7de3732c42b4f8229c001c4297b4858fac27069b4bdba854348916a8 --local_amt=100000000 --push_amt 50000000`

Check on channel status:

```
$ ln pendingchannels
$ ln listchannels
```

Create a payment request (invoice): `$ ln addinvoice --amt=6969 --memo="Time_to_ pay_the_piper!"`

Pay a payment request:

```
$ ln decodepayreq --pay_req=<PAY_REQ>
$ ln sendpayment --pay_req=<PAY_REQ>
```
