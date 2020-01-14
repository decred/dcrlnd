FROM golang:1.13-alpine as builder

# Install dependencies.
RUN apk add --no-cache --update git make

# Copy in the local repository to build from.
COPY . /go/src/github.com/decred/dcrlnd

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

RUN  cd /go/src/github.com/decred/dcrlnd \
        &&  make \
        &&  make install

# Start a new, final image to reduce size.
FROM alpine as final

# Add bash.
RUN apk add --no-cache bash

# Expose dcrlnd ports (server, rpc).
EXPOSE 9735 10009

# Copy the binaries from the builder image.
COPY --from=builder /go/bin/dcrlncli /bin/
COPY --from=builder /go/bin/dcrlnd /bin/

# Copy the entrypoint script.
COPY "docker/dcrlnd/start-dcrlnd.sh" .
RUN chmod +x start-dcrlnd.sh
