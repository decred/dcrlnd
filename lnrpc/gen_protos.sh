#!/usr/bin/env sh

build_protoc_gen_go() {

    echo "Install protoc-gen-go"
    mkdir -p bin
    export GOBIN=$PWD/bin
    go build .
    go install github.com/golang/protobuf/protoc-gen-go \
    github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway \
    github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2
}

generate() {

    GGWVERSION=$(go list -m all | grep "github.com/grpc-ecosystem/grpc-gateway " | sed 's/ /@/' -)
    PROTOSVERSION=$(go list -m all | grep "github.com/matheusd/google-protobuf-protos" | sed 's/ /@/' -)
    GOOGAPIS="$GOPATH/pkg/mod/$GGWVERSION/third_party/googleapis"
    PROTOBUFAPIS="$GOPATH/pkg/mod/$PROTOSVERSION"

    echo "Generating root gRPC server protos"

    PROTOS="lightning.proto walletunlocker.proto stateservice.proto **/*.proto"

    # For each of the sub-servers, we then generate their protos, but a restricted
    # set as they don't yet require REST proxies, or swagger docs.
    for file in $PROTOS; do
      DIRECTORY=$(dirname "${file}")
      echo "Generating protos from ${file}, into ${DIRECTORY}"

      # Generate the protos.
      protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
        --go_out=plugins=grpc,paths=source_relative:. \
        "${file}"

      # Generate the REST reverse proxy.
      annotationsFile=${file//proto/yaml}
      protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
	--grpc-gateway_out . \
	--grpc-gateway_opt logtostderr=true \
	--grpc-gateway_opt paths=source_relative \
	--grpc-gateway_opt grpc_api_configuration=${annotationsFile} \
        "${file}"

      # Finally, generate the swagger file which describes the REST API in detail.
      protoc -I. \
        -I$GOOGAPIS -I$PROTOBUFAPIS \
	--openapiv2_out . \
	--openapiv2_opt logtostderr=true \
	--openapiv2_opt grpc_api_configuration=${annotationsFile} \
	--openapiv2_opt json_names_for_fields=false \
        "${file}"
    done
}

(cd tools && build_protoc_gen_go)
PATH=$PWD/tools/bin:$PATH generate

rm -rf tools/bin
