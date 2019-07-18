module github.com/decred/dcrlnd

require (
	git.schwanenlied.me/yawning/bsaes.git v0.0.0-20190320102049-26d1add596b6 // indirect
	github.com/NebulousLabs/go-upnp v0.0.0-20181203152547-b32978b8ccbf
	github.com/Yawning/aez v0.0.0-20180408160647-ec7426b44926
	github.com/btcsuite/btcd v0.0.0-20190629003639-c26ffa870fd8
	github.com/davecgh/go-spew v1.1.1
	github.com/decred/dcrd v1.3.0
	github.com/decred/dcrd/bech32 v1.0.0
	github.com/decred/dcrd/blockchain v1.1.1
	github.com/decred/dcrd/blockchain/stake v1.1.0
	github.com/decred/dcrd/chaincfg v1.3.0
	github.com/decred/dcrd/chaincfg/chainhash v1.0.1
	github.com/decred/dcrd/connmgr v1.0.2
	github.com/decred/dcrd/dcrec v0.0.0-20190214012338-9265b4051009
	github.com/decred/dcrd/dcrec/secp256k1 v1.0.1
	github.com/decred/dcrd/dcrjson/v2 v2.0.0
	github.com/decred/dcrd/dcrutil v1.2.0
	github.com/decred/dcrd/hdkeychain v1.1.1
	github.com/decred/dcrd/mempool v1.2.0
	github.com/decred/dcrd/rpcclient/v2 v2.0.0
	github.com/decred/dcrd/txscript v1.0.2
	github.com/decred/dcrd/wire v1.2.0
	github.com/decred/dcrwallet v1.2.2
	github.com/decred/dcrwallet/chain/v2 v2.0.0
	github.com/decred/dcrwallet/errors v1.0.1
	github.com/decred/dcrwallet/wallet/v2 v2.0.0
	github.com/decred/lightning-onion v1.0.0
	github.com/decred/slog v1.0.0
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.6.4
	github.com/jackpal/gateway v1.0.5
	github.com/jackpal/go-nat-pmp v0.0.0-20170405195558-28a68d0c24ad
	github.com/jessevdk/go-flags v1.4.0
	github.com/jrick/logrotate v1.0.0
	github.com/juju/clock v0.0.0-20190205081909-9c5c9712527c // indirect
	github.com/juju/errors v0.0.0-20190207033735-e65537c515d7 // indirect
	github.com/juju/loggo v0.0.0-20190526231331-6e530bcce5d8 // indirect
	github.com/juju/retry v0.0.0-20180821225755-9058e192b216 // indirect
	github.com/juju/testing v0.0.0-20190613124551-e81189438503 // indirect
	github.com/juju/utils v0.0.0-20180820210520-bf9cc5bdd62d // indirect
	github.com/juju/version v0.0.0-20180108022336-b64dbd566305 // indirect
	github.com/kkdai/bstream v1.0.0
	github.com/miekg/dns v1.1.15
	github.com/rogpeppe/fastuuid v1.2.0 // indirect
	github.com/tv42/zbase32 v0.0.0-20190604154422-aacc64a8f915
	github.com/urfave/cli v1.20.0
	gitlab.com/NebulousLabs/fastrand v0.0.0-20181126182046-603482d69e40 // indirect
	gitlab.com/NebulousLabs/go-upnp v0.0.0-20181011194642-3a71999ed0d3 // indirect
	go.etcd.io/bbolt v1.3.2
	golang.org/x/crypto v0.0.0-20190320223903-b7391e95e576
	golang.org/x/net v0.0.0-20190125091013-d26f9f9a57f3
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4
	google.golang.org/genproto v0.0.0-20190111180523-db91494dd46c
	google.golang.org/grpc v1.18.0
	gopkg.in/errgo.v1 v1.0.1 // indirect
	gopkg.in/macaroon-bakery.v2 v2.1.0
	gopkg.in/macaroon.v2 v2.1.0
	gopkg.in/mgo.v2 v2.0.0-20180705113604-9856a29383ce // indirect
)

replace (
	github.com/decred/dcrd => github.com/decred/dcrd v0.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/bech32 => github.com/decred/dcrd/bech32 v0.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/blockchain => github.com/decred/dcrd/blockchain v0.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/connmgr => github.com/decred/dcrd/connmgr v0.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/dcrjson/v2 => github.com/decred/dcrd/dcrjson/v2 v2.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/peer => github.com/decred/dcrd/peer v0.0.0-20190306151227-8cbb5ae69df7
	github.com/decred/dcrd/rpcclient/v2 => github.com/decred/dcrd/rpcclient/v2 v2.0.0-20190306151227-8cbb5ae69df7

	github.com/decred/dcrwallet => github.com/decred/dcrwallet v0.0.0-20190322135901-7e0e5a4227d7
	github.com/decred/dcrwallet/wallet/v2 => github.com/decred/dcrwallet/wallet/v2 v2.0.0-20190322135901-7e0e5a4227d7

	github.com/decred/lightning-onion => github.com/decred/lightning-onion v0.0.0-20190321210301-95556fb4cc37
)
