//go:build kvdb_etcd
// +build kvdb_etcd

package kvdb

import (
	"context"

	"github.com/decred/dcrlnd/channeldb/kvdb/etcd"
)

// TestBackend is conditionally set to etcd when the kvdb_etcd build tag is
// defined, allowing testing our database code with etcd backend.
const TestBackend = EtcdBackendName

// GetEtcdBackend returns an etcd backend configured according to the
// passed etcdConfig.
func GetEtcdBackend(ctx context.Context, etcdConfig *etcd.Config) (
	Backend, error) {

	return Open(EtcdBackendName, etcdConfig)
}

// GetEtcdTestBackend creates an embedded etcd backend for testing
// storig the database at the passed path.
func GetEtcdTestBackend(path string, clientPort, peerPort uint16) (
	Backend, func(), error) {

	empty := func() {}

	config, cleanup, err := etcd.NewEmbeddedEtcdInstance(
		path, clientPort, peerPort,
	)
	if err != nil {
		return nil, empty, err
	}

	backend, err := Open(EtcdBackendName, context.Background(), config)
	if err != nil {
		cleanup()
		return nil, empty, err
	}

	return backend, cleanup, nil
}
