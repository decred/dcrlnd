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
# user override it during container start.
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
DEBUG=$(set_default "$DEBUG" "debug")
NETWORK=$(set_default "$NETWORK" "simnet")
BACKEND="dcrd"

PARAMS=""
if [ "$NETWORK" != "mainnet" ]; then
   PARAMS=$(echo --$NETWORK)
fi

# CAUTION: DO NOT use the --noseedback for production/mainnet setups, ever!
# Also, setting --rpclisten to 0.0.0.0 will cause it to listen on an IP
# address that is reachable on the internal network. If you do this outside of
# docker, this might be a security concern!

PARAMS=$(echo $PARAMS \
    --noseedbackup \
    --logdir=/data \
    --node=dcrd \
    "--$BACKEND.rpccert"="/rpc/rpc.cert" \
    "--$BACKEND.rpchost"="dcrd" \
    "--$BACKEND.rpcuser"="$RPCUSER" \
    "--$BACKEND.rpcpass"="$RPCPASS" \
    --rpclisten=0.0.0.0 \
    "--rpclisten=localhost:10009" \
    --restlisten=0.0.0.0 \
    --listen=0.0.0.0 \
    --debuglevel="$DEBUG" \
    "$@"
)

echo "Run dcrlnd $PARAMS"
exec dcrlnd $PARAMS
