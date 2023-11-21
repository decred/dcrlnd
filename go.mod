module github.com/decred/dcrlnd

go 1.18

require (
	decred.org/dcrwallet/v3 v3.0.0
	github.com/NebulousLabs/go-upnp v0.0.0-20181203152547-b32978b8ccbf
	github.com/Yawning/aez v0.0.0-20211027044916-e49e68abd344
	github.com/btcsuite/btcwallet/walletdb v1.4.0
	github.com/davecgh/go-spew v1.1.1
	github.com/decred/dcrd/addrmgr/v2 v2.0.2
	github.com/decred/dcrd/bech32 v1.1.3
	github.com/decred/dcrd/blockchain/stake/v5 v5.0.0
	github.com/decred/dcrd/blockchain/standalone/v2 v2.2.0
	github.com/decred/dcrd/chaincfg/chainhash v1.0.4
	github.com/decred/dcrd/chaincfg/v3 v3.2.0
	github.com/decred/dcrd/connmgr v1.1.1
	github.com/decred/dcrd/dcrec v1.0.1
	github.com/decred/dcrd/dcrec/secp256k1/v2 v2.0.1
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.2.0
	github.com/decred/dcrd/dcrjson/v4 v4.0.1
	github.com/decred/dcrd/dcrutil/v4 v4.0.1
	github.com/decred/dcrd/gcs/v3 v3.0.1
	github.com/decred/dcrd/gcs/v4 v4.0.0
	github.com/decred/dcrd/hdkeychain/v3 v3.1.1
	github.com/decred/dcrd/rpc/jsonrpc/types/v4 v4.0.0
	github.com/decred/dcrd/rpcclient/v8 v8.0.0
	github.com/decred/dcrd/txscript/v4 v4.1.0
	github.com/decred/dcrd/wire v1.6.0
	github.com/decred/dcrtest/dcrdtest v1.0.0
	github.com/decred/lightning-onion/v4 v4.0.0
	github.com/decred/slog v1.2.0
	github.com/go-errors/errors v1.4.2
	github.com/golang/protobuf v1.5.3
	github.com/gorilla/websocket v1.5.0
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/jackpal/gateway v1.0.10
	github.com/jackpal/go-nat-pmp v1.0.2
	github.com/jedib0t/go-pretty v4.3.0+incompatible
	github.com/jessevdk/go-flags v1.5.0
	github.com/jrick/logrotate v1.0.0
	github.com/kkdai/bstream v1.0.0
	github.com/matheusd/etcd v0.5.0-alpha.5.0.20230703143131-79b93c0cee87
	github.com/matheusd/google-protobuf-protos v0.0.0-20200707194502-ef6ec5c2266f
	github.com/matheusd/protobuf-hex-display v1.3.3-0.20201012153224-75fb8d4840f1
	github.com/miekg/dns v1.1.53
	github.com/prometheus/client_golang v1.15.0
	github.com/stretchr/testify v1.8.1
	github.com/tv42/zbase32 v0.0.0-20220222190657-f76a9fc892fa
	github.com/urfave/cli v1.20.0
	go.etcd.io/bbolt v1.3.8
	golang.org/x/crypto v0.15.0
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa
	golang.org/x/net v0.18.0
	golang.org/x/sys v0.14.0
	golang.org/x/time v0.1.0
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2
	google.golang.org/grpc v1.54.0
	google.golang.org/protobuf v1.30.0
	gopkg.in/macaroon-bakery.v2 v2.3.0
	gopkg.in/macaroon.v2 v2.1.0
	matheusd.com/testctx v0.1.0
)

require (
	decred.org/cspp/v2 v2.1.0 // indirect
	github.com/agl/ed25519 v0.0.0-20170116200512-5312a6153412 // indirect
	github.com/asaskevich/govalidator v0.0.0-20190424111038-f61b66f89f4a // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/companyzero/sntrup4591761 v0.0.0-20220309191932-9e0f3af2f07a // indirect
	github.com/coreos/bbolt v1.3.3 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/coreos/pkg v0.0.0-20230327231512-ba87abf18a23 // indirect
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/decred/base58 v1.0.5 // indirect
	github.com/decred/dcrd v1.8.0 // indirect
	github.com/decred/dcrd/blockchain/stake/v4 v4.0.0 // indirect
	github.com/decred/dcrd/certgen v1.1.2 // indirect
	github.com/decred/dcrd/chaincfg v1.5.2 // indirect
	github.com/decred/dcrd/connmgr/v3 v3.1.1 // indirect
	github.com/decred/dcrd/container/apbf v1.0.1 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.0.1 // indirect
	github.com/decred/dcrd/crypto/ripemd160 v1.0.2 // indirect
	github.com/decred/dcrd/database/v3 v3.0.1 // indirect
	github.com/decred/dcrd/dcrec/edwards/v2 v2.0.3 // indirect
	github.com/decred/dcrd/lru v1.1.2 // indirect
	github.com/decred/dcrd/math/uint256 v1.0.1 // indirect
	github.com/decred/dcrd/peer/v3 v3.0.2 // indirect
	github.com/decred/go-socks v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ghodss/yaml v1.0.0 // indirect
	github.com/go-macaroon-bakery/macaroonpb v1.0.0 // indirect
	github.com/go-openapi/errors v0.19.2 // indirect
	github.com/go-openapi/strfmt v0.19.5 // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.0 // indirect
	github.com/golang/glog v1.0.0 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/jonboulle/clockwork v0.4.0 // indirect
	github.com/jrick/bitset v1.0.0 // indirect
	github.com/jrick/wsrpc/v2 v2.3.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/mattn/go-runewidth v0.0.9 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/mitchellh/mapstructure v1.1.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.42.0 // indirect
	github.com/prometheus/procfs v0.9.0 // indirect
	github.com/rogpeppe/fastuuid v1.2.0 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	github.com/soheilhy/cmux v0.1.5 // indirect
	github.com/stretchr/objx v0.5.0 // indirect
	github.com/syndtr/goleveldb v1.0.1-0.20210819022825-2ae1ddf74ef7 // indirect
	github.com/tmc/grpc-websocket-proxy v0.0.0-20220101234140-673ab2c3ae75 // indirect
	github.com/xiang90/probing v0.0.0-20221125231312-a49e3df8f510 // indirect
	gitlab.com/NebulousLabs/fastrand v0.0.0-20181126182046-603482d69e40 // indirect
	gitlab.com/NebulousLabs/go-upnp v0.0.0-20211002182029-11da932010b6 // indirect
	gitlab.com/yawning/bsaes.git v0.0.0-20190805113838-0a714cd429ec // indirect
	go.mongodb.org/mongo-driver v1.5.1 // indirect
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	golang.org/x/mod v0.14.0 // indirect
	golang.org/x/sync v0.5.0 // indirect
	golang.org/x/term v0.14.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1 // indirect
	gopkg.in/errgo.v1 v1.0.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	lukechampine.com/blake3 v1.2.1 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)
