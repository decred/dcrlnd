#!/usr/bin/env bash

# exit from script if error was raised.
set -e

# error function is used within a bash function in order to send the error
# message directly to the stderr output and exit.
error() {
    echo "$1" > /dev/stderr
    exit 0
}

# return is used within bash function in order to return the value.
return() {
    echo "$1"
}

# set_default function gives the ability to move the setting of default
# env variable from docker file to the script thereby giving the ability to the
# user override it durin container start.
set_default() {
    # docker initialized env variables with blank string and we can't just
    # use -z flag as usually.
    BLANK_STRING='""'

    VARIABLE="$1"
    DEFAULT="$2"

    if [[ -z "$VARIABLE" || "$VARIABLE" == "$BLANK_STRING" ]]; then

        if [ -z "$DEFAULT" ]; then
            error "You should specify default variable"
        else
            VARIABLE="$DEFAULT"
        fi
    fi

   return "$VARIABLE"
}

# Set default variables if needed.
RPCUSER=$(set_default "$RPCUSER" "devuser")
RPCPASS=$(set_default "$RPCPASS" "devpass")
DEBUG=$(set_default "$DEBUG" "info")
NETWORK=$(set_default "$NETWORK" "simnet")

PARAMS=""
if [ "$NETWORK" != "mainnet" ]; then
   PARAMS=$(echo --$NETWORK)
fi

PARAMS=$(echo $PARAMS \
    "--configfile=/data/dcrd.conf" \
    "--debuglevel=$DEBUG" \
    "--rpcuser=$RPCUSER" \
    "--rpcpass=$RPCPASS" \
    "--datadir=/data" \
    "--logdir=/data" \
    "--rpccert=/config/rpc.cert" \
    "--rpckey=/config/rpc.key" \
    "--rpclisten=0.0.0.0" \
    "--txindex"
)

# Set the mining flag only if address is non empty.
if [[ -n "$MINING_ADDRESS" ]]; then
    PARAMS="$PARAMS --miningaddr=$MINING_ADDRESS"
fi

# Create the dcrctl.conf based in the updated variables.
if [ ! -f "/root/.dcrctl/dcrctl.conf" ]; then
mkdir /root/.dcrctl
cat > "/root/.dcrctl/dcrctl.conf" <<EOF
rpcuser=${RPCUSER}
rpcpass=${RPCPASS}
rpccert="/config/rpc.cert"
walletrpcserver=dcrwallet
EOF

fi

# Add user parameters to command.
PARAMS="$PARAMS $@"

# Print command and start decred node.
echo "Command: dcrd $PARAMS"
exec dcrd $PARAMS
