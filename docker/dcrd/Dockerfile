FROM golang:1.11-alpine as builder

MAINTAINER Olaoluwa Osuntokun <laolu@lightning.network>

# Install build dependencies such as git and glide.
RUN apk add --no-cache git gcc musl-dev

WORKDIR $GOPATH/src/github.com/decred/dcrd

# Grab and install the latest version of of dcrd and all related dependencies.
RUN git clone https://github.com/decred/dcrd.git . \
    &&  GO111MODULE=on go install -v . ./cmd/...

# Start a new image
FROM alpine as final

# Expose mainnet ports (server, rpc)
EXPOSE 9108 9109

# Expose testnet ports (server, rpc)
EXPOSE 19108 19109

# Expose simnet ports (server, rpc)
EXPOSE 18555 19556

# Copy the compiled binaries from the builder image.
COPY --from=builder /go/bin/addblock /bin/
COPY --from=builder /go/bin/dcrctl /bin/
COPY --from=builder /go/bin/dcrd /bin/
COPY --from=builder /go/bin/findcheckpoint /bin/
COPY --from=builder /go/bin/gencerts /bin/

COPY "start-dcrctl.sh" .
COPY "start-dcrd.sh" .

RUN apk add --no-cache \
    bash \
    ca-certificates \
&&  mkdir "/rpc" "/root/.dcrd" "/root/.dcrctl" \
&&  touch "/root/.dcrd/dcrd.conf" \
&&  chmod +x start-dcrctl.sh \
&&  chmod +x start-dcrd.sh \
# Manually generate certificate and add all domains, it is needed to connect
# "dcrctl" and "dcrlnd" to "dcrd" over docker links.
&& "/bin/gencerts" --host="*" --directory="/rpc" --force

# Create a volume to house pregenerated RPC credentials. This will be
# shared with any dcrlnd, dcrctl containers so they can securely query dcrd's RPC
# server.
# You should NOT do this before certificate generation!
# Otherwise manually generated certificate will be overridden with shared
# mounted volume! For more info read dockerfile "VOLUME" documentation.
VOLUME ["/rpc"]
