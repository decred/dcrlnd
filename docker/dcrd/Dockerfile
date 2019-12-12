FROM golang:1.13-alpine as builder

# Install build dependencies.
RUN apk add --no-cache git

WORKDIR $GOPATH/src/github.com/decred/dcrd

# Grab and install the latest version of of dcrd and all related dependencies.
RUN git clone https://github.com/decred/dcrd.git . \
    &&  go install . ./cmd/...

# Start a new image
FROM alpine as final

RUN apk add --no-cache \
    bash \
    ca-certificates

# Copy the compiled binaries from the builder image.
COPY --from=builder /go/bin/dcrd /bin/
COPY --from=builder /go/bin/dcrctl /bin/
COPY --from=builder /go/bin/findcheckpoint /bin/
COPY --from=builder /go/bin/gencerts /bin/
COPY --from=builder /go/bin/addblock /bin/

COPY "start-dcrd.sh" .
COPY "start-dcrctl.sh" .

RUN mkdir "/rpc" \
    &&  chmod +x start-dcrctl.sh \
    &&  chmod +x start-dcrd.sh \
    && "/bin/gencerts" --host="*" --directory="/rpc" --force

# Create a volume to house pregenerated RPC credentials. This will be
# shared with any containers so they can securely query dcrd's RPC server.
# You should NOT do this before certificate generation!
# Otherwise manually generated certificate will be overridden with shared
# mounted volume! For more info read dockerfile "VOLUME" documentation.
VOLUME ["/rpc"]

# Expose mainnet ports (server, rpc)
EXPOSE 9108 9109

# Expose testnet ports (server, rpc)
EXPOSE 19108 19109

# Expose simnet ports (server, rpc)
EXPOSE 18555 19556
