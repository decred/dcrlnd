FROM golang:1.20-alpine as builder

# Install build dependencies.
RUN apk add --no-cache git

WORKDIR $GOPATH/src/github.com/decred

ARG DCRD_VERSION=release-v1.7.7
ARG DCRCTL_VERSION=release-v1.7.7

# Grab and install the latest version of dcrd and all related dependencies.
RUN git clone -b $DCRD_VERSION https://github.com/decred/dcrd.git \
    && cd dcrd/ \
    &&  go install . ./...

# Grab and install the latest version of dcrctl and all related dependencies.
RUN git clone -b $DCRCTL_VERSION https://github.com/decred/dcrctl.git \
    && cd dcrctl/ \
    &&  go install

# Start a new image
FROM alpine as final

RUN apk add --no-cache \
    bash \
    ca-certificates

# Copy the compiled binaries from the builder image.
COPY --from=builder /go/bin/* /bin/

COPY "start-dcrd.sh" .

# Generate an certificate for both dcrd and dcrwallet.
RUN mkdir "/config" \
    &&  chmod +x start-dcrd.sh \
    && "/bin/gencerts" -H="dcrd" -H="dcrwallet" -f -L /config/rpc.cert /config/rpc.key

# Create a volume to house pregenerated RPC credentials. This will be
# shared with any containers so they can securely query dcrd's RPC server.
# You should NOT do this before certificate generation!
# Otherwise manually generated certificate will be overridden with shared
# mounted volume! For more info read dockerfile "VOLUME" documentation.
VOLUME ["/config"]

# Expose mainnet ports (server, rpc)
EXPOSE 9108 9109

# Expose testnet ports (server, rpc)
EXPOSE 19108 19109

# Expose simnet ports (server, rpc)
EXPOSE 18555 19556
