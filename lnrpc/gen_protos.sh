#!/usr/bin/env sh

build_protoc_gen_go() {

    echo "Install protoc-gen-go"
    mkdir -p bin
    export GOBIN=$PWD/bin
    go build .
    go install github.com/golang/protobuf/protoc-gen-go \
    github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway \
    github.com/grpc-ecosystem/grpc-gateway/protoc-gen-swagger
    
}

generate() {

    GGWVERSION=$(go list -m all | grep "github.com/grpc-ecosystem/grpc-gateway" | sed 's/ /@/' -)
    PROTOSVERSION=$(go list -m all | grep "github.com/matheusd/google-protobuf-protos" | sed 's/ /@/' -)
    GOOGAPIS="$GOPATH/pkg/mod/$GGWVERSION/third_party/googleapis"
    PROTOBUFAPIS="$GOPATH/pkg/mod/$PROTOSVERSION"

    echo "Generating root gRPC server protos"
    # Generate the protos.
    protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
        --go_out=plugins=grpc,paths=source_relative:. \
        rpc.proto

    # Generate the REST reverse proxy.
    protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
        --grpc-gateway_out=logtostderr=true,paths=source_relative:. \
        rpc.proto

    # Finally, generate the swagger file which describes the REST API in detail.
    protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
        --swagger_out=logtostderr=true:. \
        rpc.proto

    # For each of the sub-servers, we then generate their protos, but a restricted
    # set as they don't yet require REST proxies, or swagger docs.
    for file in **/*.proto
    do
        DIRECTORY=$(dirname ${file})
        echo "Generating protos from ${file}, into ${DIRECTORY}"

        protoc -I. \
            -I$GOOGAPIS -I$PROTOBUFAPIS \
            --go_out=plugins=grpc,paths=source_relative:. \
            ${file}
        done
}

(cd tools && build_protoc_gen_go)
PATH=$PWD/tools/bin:$PATH generate

rm -rf tools/bin
