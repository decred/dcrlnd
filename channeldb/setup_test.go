package channeldb

import (
	"testing"

	"github.com/decred/dcrlnd/kvdb"
)

func TestMain(m *testing.M) {
	kvdb.RunTests(m)
}
