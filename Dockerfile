FROM golang:1.13-alpine as builder

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

# Install dependencies and build the binaries.
RUN apk add --no-cache --update alpine-sdk \
    git \
    make \
    gcc \
&&  git clone https://github.com/decred/dcrlnd /go/src/github.com/decred/dcrlnd \
&&  cd /go/src/github.com/decred/dcrlnd \
&&  make \
&&  make install

# Start a new, final image.
FROM alpine as final

# Define a root volume for data persistence.
VOLUME /root/.dcrlnd

# Add bash and ca-certs, for quality of life and SSL-related reasons.
RUN apk --no-cache add \
    bash \
    ca-certificates

# Copy the binaries from the builder image.
COPY --from=builder /go/bin/dcrlncli /bin/
COPY --from=builder /go/bin/dcrlnd /bin/

# Expose lnd ports (p2p, rpc).
EXPOSE 9735 10009

# Specify the start command and entrypoint as the dcrlnd daemon.
ENTRYPOINT ["dcrlnd"]
