#!/bin/sh
#
# Copyright (c) 2021 The Decred developers
# Use of this source code is governed by an ISC
# license that can be found in the LICENSE file.
#
# Minimal dcrln setup for a three-node simnet network.
#
# This sets up a decred miner + voting wallet to keep the network going, then
# prepares three dcrlnd nodes by importing hard-coded seeds, opening and
# confirming bidirectional channels between them.
#
# The final layout of the dcrln network after this runs is (modulo fees):
#
#  dcrlnd0 < 0.05 --- 0.05 > dcrlnd1 < 0.03 --- 0.07 > dcrlnd2

set -e
set -x

NODES_ROOT=~/dcrlndsimnetnodes
DCRLND_BIN="dcrlnd"
SESSION="dcrlnd-3node"
RPCUSER="USER"
RPCPASS="PASS"
WALLET_SEED="1111111111111111111111111111111111111111111111111111111111111111"
WALLET_MINING_ADDR="Ssoaqgx4ecmHX54LqrUXgqi6miUFxP9iUvc" # NOTE: This must be changed if the seed is changed.
WALLET_XFER_ADDR="Ssoaqgx4ecmHX54LqrUXgqi6miUFxP9iUvc" # same as above
WALLET_CREATE_CONFIG="y
n
y
${WALLET_SEED}
"
NODE0_SEED="abandon replace vendor festival curious load vague empty noise level sock brain noodle nominee concert resemble rice pilot gentle beyond carry material birth town"
NODE1_SEED="able field segment load sister riot carbon acoustic undo history zebra multiply blouse raise radar radio gloom slight vote warrior water stable hub village"
NODE2_SEED="able found student horse wife gas catch jelly blast grab wage strategy toward can empty junior pond medal cave wise argue typical gossip decline"
NODE0_PUBID="032b8c752fdcd8da420e6fd5d2317cfc42f51a0928d34511fce7b6ba42e1e353b8"
NODE1_PUBID="03bb9246b8eaacde90c3b9e7a0539b0b70cde514ec0d2571c68063ac15edac5534"
NODE2_PUBID="029398ddb14e4b3cb92fc64d61fcaaa2f3b590951b0b05ba1ecc04a7504d333213"

alias tmxsend="tmux send-keys"

if [ -d "${NODES_ROOT}" ] ; then
  rm -R "${NODES_ROOT}"
fi

mkdir -p "${NODES_ROOT}/"{dcrd,wallet,dcrlnd0,dcrlnd1,dcrlnd2}

cat > "${NODES_ROOT}/dcrd.conf" <<EOF
rpcuser = ${RPCUSER}
rpcpass = ${RPCPASS}
simnet = 1
logdir = ./log
datadir = ./data
txindex = 1
listen = :19555
rpclisten = :19556
miningaddr = ${WALLET_MINING_ADDR}
; debuglevel=TXMP=TRACE,MINR=TRACE,CHAN=TRACE
EOF

cat > "${NODES_ROOT}/dcrctl.conf" <<EOF
rpcuser = ${RPCUSER}
rpcpass = ${RPCPASS}
simnet = 1
EOF

cat > "${NODES_ROOT}/wallet.conf" <<EOF
username = ${RPCUSER}
password = ${RPCPASS}
simnet = 1
logdir = ./log
appdata = ./data
pass = 123
enablevoting = 1
enableticketbuyer = 1
rpcconnect = 127.0.0.1:19556
ticketbuyer.limit = 5
debuglevel = debug
EOF

cat > "${NODES_ROOT}/dcrdata.conf" <<EOF
dcrdserv = localhost:19556
dcrduser = ${RPCUSER}
dcrdpass = ${RPCPASS}
appdata = ${NODES_ROOT}/dcrdata
simnet = 1
EOF

cat > "${NODES_ROOT}/dcrlnd0.conf" <<EOF
[Application Options]

datadir = ${NODES_ROOT}/dcrlnd0
logdir = ${NODES_ROOT}/dcrlnd0/logs
tlscertpath = ${NODES_ROOT}/dcrlnd0/tls.cert
tlskeypath = ${NODES_ROOT}/dcrlnd0/tls.key
rpclisten = 127.0.0.1:20000
restlisten = 127.0.0.1:20001
listen = 127.0.0.1:20002
maxlogfiles = 100


debuglevel = debug

node = "dcrd"
simnet = 1

[dcrd]
dcrd.rpchost = localhost:19556
dcrd.rpcuser = ${RPCUSER}
dcrd.rpcpass = ${RPCPASS}
EOF

cat > "${NODES_ROOT}/dcrlnd1.conf" <<EOF
[Application Options]

datadir = ${NODES_ROOT}/dcrlnd1
logdir = ${NODES_ROOT}/dcrlnd1/logs
tlscertpath = ${NODES_ROOT}/dcrlnd1/tls.cert
tlskeypath = ${NODES_ROOT}/dcrlnd1/tls.key
rpclisten = 127.0.0.1:20100
restlisten = 127.0.0.1:20101
listen = 127.0.0.1:20102
maxlogfiles = 100

debuglevel = debug

node = "dcrd"
simnet = 1

[dcrd]
dcrd.rpchost = localhost:19556
dcrd.rpcuser = ${RPCUSER}
dcrd.rpcpass = ${RPCPASS}
EOF

cat > "${NODES_ROOT}/dcrlnd2.conf" <<EOF
[Application Options]

datadir = ${NODES_ROOT}/dcrlnd2
logdir = ${NODES_ROOT}/dcrlnd2/logs
tlscertpath = ${NODES_ROOT}/dcrlnd2/tls.cert
tlskeypath = ${NODES_ROOT}/dcrlnd2/tls.key
rpclisten = 127.0.0.1:20200
restlisten = 127.0.0.1:20201
listen = 127.0.0.1:20202
maxlogfiles = 100

debuglevel = debug

node = "dcrd"
simnet = 1

[dcrd]
dcrd.rpchost = localhost:19556
dcrd.rpcuser = ${RPCUSER}
dcrd.rpcpass = ${RPCPASS}
EOF


# Scripts

cat > "${NODES_ROOT}/dcrd/ctl" <<EOF
#!/bin/sh
dcrctl -C ../dcrctl.conf \$*
EOF
chmod +x "${NODES_ROOT}/dcrd/ctl"

cat > "${NODES_ROOT}/dcrd/mine" <<EOF
#!/bin/sh
NUM=1
case \$1 in
    ''|*[!0-9]*)  ;;
    *) NUM=\$1 ;;
esac

for i in \$(seq \$NUM) ; do
  dcrctl -C ../dcrctl.conf generate 1
  sleep 0.3
done
EOF
chmod +x "${NODES_ROOT}/dcrd/mine"

cat > "${NODES_ROOT}/wallet/ctl" <<EOF
#!/bin/sh
dcrctl -C ../dcrctl.conf --wallet -c ./data/rpc.cert \$*
EOF
chmod +x "${NODES_ROOT}/wallet/ctl"

cat > "${NODES_ROOT}/wallet/tickets" <<EOF
#!/bin/sh
NUM=1
case \$1 in
    ''|*[!0-9]*) ;;
    *) NUM=\$1 ;;
esac

./ctl purchaseticket default 999999 1 \`./ctl getnewaddress\` \$NUM
EOF
chmod +x "${NODES_ROOT}/wallet/tickets"

cat > "${NODES_ROOT}/wallet/xfer" <<EOF
#!/bin/sh
./ctl sendtoaddress ${WALLET_XFER_ADDR} 0.1
EOF
chmod +x "${NODES_ROOT}/wallet/xfer"


cat > "${NODES_ROOT}/dcrlnd0/ctl" <<EOF
#!/bin/sh
dcrlncli \\
  --simnet \\
  --chain decred \\
  --rpcserver localhost:20000 \\
  --lnddir ${NODES_ROOT}/dcrlnd0 \\
  --tlscertpath ${NODES_ROOT}/dcrlnd0/tls.cert \\
  --macaroonpath ${NODES_ROOT}/dcrlnd0/chain/decred/simnet/admin.macaroon \\
  \$*
EOF
chmod +x "${NODES_ROOT}/dcrlnd0/ctl"


cat > "${NODES_ROOT}/dcrlnd1/ctl" <<EOF
#!/bin/sh
dcrlncli \\
  --simnet \\
  --chain decred \\
  --rpcserver localhost:20100 \\
  --lnddir ${NODES_ROOT}/dcrlnd1 \\
  --tlscertpath ${NODES_ROOT}/dcrlnd1/tls.cert \\
  --macaroonpath ${NODES_ROOT}/dcrlnd1/chain/decred/simnet/admin.macaroon \\
  \$*
EOF
chmod +x "${NODES_ROOT}/dcrlnd1/ctl"

cat > "${NODES_ROOT}/dcrlnd2/ctl" <<EOF
#!/bin/sh
dcrlncli \\
  --simnet \\
  --chain decred \\
  --rpcserver localhost:20200 \\
  --lnddir ${NODES_ROOT}/dcrlnd2 \\
  --tlscertpath ${NODES_ROOT}/dcrlnd2/tls.cert \\
  --macaroonpath ${NODES_ROOT}/dcrlnd2/chain/decred/simnet/admin.macaroon \\
  \$*
EOF
chmod +x "${NODES_ROOT}/dcrlnd2/ctl"

function countdown {
  secs=$1
  while [ $secs -gt 0 ]; do
    echo -ne "$secs \033[0K\r"
    sleep 1
    : $((secs--))
  done
}


# Start Session.

cd ${NODES_ROOT} && tmux -2 new-session -d -s $SESSION
tmxsend "alias a=${NODES_ROOT}/dcrlnd0/ctl" C-m
tmxsend "alias b=${NODES_ROOT}/dcrlnd1/ctl" C-m
tmxsend "alias c=${NODES_ROOT}/dcrlnd2/ctl" C-m


# Setup mining node.

tmux new-window -t $SESSION:1 -n 'network'
tmux split-window -h
tmux split-window -v
tmux select-pane -t 0
tmux split-window -v
tmux select-pane -t 0

tmxsend "cd dcrd" C-m
tmxsend "dcrd -C ../dcrd.conf" C-m
tmux select-pane -t 1
tmxsend "cd dcrd" C-m
sleep 3
tmxsend "./ctl generate 32" C-m
sleep 1

# Setup voting wallet.

tmux select-pane -t 2
tmxsend "cd wallet" C-m
tmux send-keys -t $SESSION "echo \"${WALLET_CREATE_CONFIG}\" | dcrwallet -C ../wallet.conf --create; tmux wait-for -S wallet" C-m
tmux wait-for "wallet"
sleep 1
tmxsend "dcrwallet -C ../wallet.conf" C-m
tmux select-pane -t 3
tmxsend "cd wallet" C-m


# Bring up ln nodes.

tmux new-window -t $SESSION:2 -n 'dcrlnd0'
tmxsend "${DCRLND_BIN} --configfile ${NODES_ROOT}/dcrlnd0.conf" C-m
tmux split-window -v
tmxsend "cd ${NODES_ROOT}/dcrlnd0" C-m

tmux new-window -t $SESSION:3 -n 'dcrlnd1'
tmxsend "${DCRLND_BIN} --configfile ${NODES_ROOT}/dcrlnd1.conf" C-m
tmux split-window -v
tmxsend "cd ${NODES_ROOT}/dcrlnd1" C-m

tmux new-window -t $SESSION:4 -n 'dcrlnd2'
tmxsend "${DCRLND_BIN} --configfile ${NODES_ROOT}/dcrlnd2.conf" C-m
tmux split-window -v
tmxsend "cd ${NODES_ROOT}/dcrlnd2" C-m

# Wait for nodes to query for pwd.
echo "Waiting for dcrlnd nodes to initialize"
countdown 5

# Create lnd wallets.

tmux select-window -t 2
tmxsend "./ctl create" C-m
sleep 3
tmxsend "12345678" C-m "12345678" C-m

tmux select-window -t 3
tmxsend "./ctl create" C-m
sleep 3
tmxsend "12345678" C-m "12345678" C-m

tmux select-window -t 4
tmxsend "./ctl create" C-m
sleep 3
tmxsend "12345678" C-m "12345678" C-m

# Wait for seed input.
echo "Waiting to input seed"
countdown 3


# Seed input lnd wallets.
tmux select-window -t 2
tmxsend "y" C-m
sleep 2
tmxsend "${NODE0_SEED}" C-m C-m C-m
tmux select-window -t 3
tmxsend "y" C-m
sleep 2
tmxsend "${NODE1_SEED}" C-m C-m C-m
tmux select-window -t 4
tmxsend "y" C-m
sleep 2
tmxsend "${NODE2_SEED}" C-m C-m C-m

echo "Waiting for nodes to sync up"
countdown 5

# Send coins to all nodes and mine them.
addr0=`${NODES_ROOT}/dcrlnd0/ctl newaddress p2pkh | jq .address`
addr1=`${NODES_ROOT}/dcrlnd1/ctl newaddress p2pkh | jq .address`
addr2=`${NODES_ROOT}/dcrlnd2/ctl newaddress p2pkh | jq .address`
tmux select-window -t 1
tmxsend "./ctl sendtoaddress $addr0 10" C-m
tmxsend "./ctl sendtoaddress $addr1 10" C-m
tmxsend "./ctl sendtoaddress $addr2 10" C-m
tmux select-pane -t 1
tmxsend "./mine 3" C-m

echo "Waiting for lnd wallets to catch up"
countdown 3

# Connect nodes and open a channel node0 => node1.
tmux select-window -t 2
sleep 0.5
tmxsend "./ctl connect ${NODE1_PUBID}@127.0.0.1:20102" C-m
tmxsend "./ctl openchannel ${NODE1_PUBID} 10000000 5000000" C-m

# Connect nodes and open a channel node2 => node1.
tmux select-window -t 4
sleep 0.5
tmxsend "./ctl connect ${NODE1_PUBID}@127.0.0.1:20102" C-m
tmxsend "./ctl openchannel ${NODE1_PUBID} 10000000 3000000" C-m

echo "Waiting for funding txs to be broadcast"
countdown 3

# Mine the new channel txs
tmux select-window -t 1
tmxsend "./mine 8" C-m

echo "Attaching to session '$SESSION'"
tmux attach-session
