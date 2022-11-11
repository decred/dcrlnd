module config

go 1.19

require (
	decred.org/dcrwallet/v3 v3.0.0-20221109165347-f31e848f2697
	google.golang.org/grpc v1.45.0
)

require (
	github.com/golang/protobuf v1.5.2 // indirect
	golang.org/x/net v0.6.0 // indirect
	golang.org/x/sys v0.5.0 // indirect
	golang.org/x/text v0.7.0 // indirect
	google.golang.org/genproto v0.0.0-20200526211855-cb27e3aa2013 // indirect
	google.golang.org/protobuf v1.27.1 // indirect
)

replace (
	github.com/decred/dcrd/blockchain/stake/v5 => github.com/decred/dcrd/blockchain/stake/v5 v5.0.0-20221022042529-0a0cc3b3bf92
	github.com/decred/dcrd/gcs/v4 => github.com/decred/dcrd/gcs/v4 v4.0.0-20221022042529-0a0cc3b3bf92
	github.com/decred/dcrd/rpc/jsonrpc/types/v4 => github.com/decred/dcrd/rpc/jsonrpc/types/v4 v4.0.0-20221022042529-0a0cc3b3bf92
	github.com/decred/dcrd/rpcclient/v8 => github.com/decred/dcrd/rpcclient/v8 v8.0.0-20221022042529-0a0cc3b3bf92
)
