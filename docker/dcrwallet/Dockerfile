FROM golang:1.13-alpine as builder

# Passing env to the builder image
ARG WALLET_SEED
ARG WALLET_PASS
ENV WALLET_SEED=$WALLET_SEED WALLET_PASS=$WALLET_PASS

# Install build dependencies.
RUN apk add --no-cache git

WORKDIR $GOPATH/src/github.com/decred/dcrwallet
# Grab and install the latest version of of dcrwallet
# and all related dependencies.
RUN git clone https://github.com/decred/dcrwallet.git . \
    &&  go install

COPY . /init

# Start dcrwallet to create new wallet, get an address
# and export the walletdb
RUN dcrwallet \
    --noinitialload \
    --appdata=/data \
    --rpccert=/rpc/rpc.cert \
    --rpckey=/rpc/rpc.key \
    --simnet \
    & sleep 5 \
    && cd /init \
    && go run main.go

# Start a new image
FROM alpine as final

# Passing argument to final image
ARG WALLET_PASS
ENV WALLET_PASS=$WALLET_PASS

RUN apk add --no-cache \
    bash \
    ca-certificates

RUN mkdir "/data"
# Copy the compiled binaries and db from the builder image.
COPY --from=builder /go/bin/dcrwallet /bin/
COPY --from=builder /data/simnet /data/simnet
COPY --from=builder /data/dcrd.conf /data/dcrd.conf

COPY "start-dcrwallet.sh" .
RUN chmod +x start-dcrwallet.sh 

VOLUME ["/data"]

# Expose simnet ports (server, rpc, grpc)
EXPOSE 19556-19558
