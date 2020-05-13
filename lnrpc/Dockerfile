FROM golang:1.14-buster

RUN apt update && apt install -y git protobuf-compiler

WORKDIR /build/lnrpc
CMD ["/bin/bash", "gen_protos.sh"]
