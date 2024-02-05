package dcrmigration01

import (
	"testing"

	"github.com/decred/dcrlnd/channeldb/migtest"
	"github.com/decred/dcrlnd/kvdb"
)

var (
	hexStr = migtest.Hex

	tlvOutpointOpen   = hexStr("000100")
	tlvOutpointClosed = hexStr("000101")

	outpointMig20     = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c7")
	outpointMig20_2   = hexStr("abb637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c7")
	outpointDataMig20 = map[string]interface{}{
		outpointMig20:   tlvOutpointOpen,
		outpointMig20_2: tlvOutpointClosed,
	}

	outpointCorrect     = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c700")
	outpointCorrect_2   = hexStr("abb637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c700")
	outpointDataCorrect = map[string]interface{}{
		outpointCorrect:   tlvOutpointOpen,
		outpointCorrect_2: tlvOutpointClosed,
	}
)

func TestFixMigration20(t *testing.T) {
	// Prime the database with the results of migration20 (wrong outpoint
	// key).
	before := func(tx kvdb.RwTx) error {
		return migtest.RestoreDB(tx, outpointBucket, outpointDataMig20)
	}

	// Double check the keys were migrated to use the correct serialization
	// of outpoint.
	after := func(tx kvdb.RwTx) error {
		return migtest.VerifyDB(tx, outpointBucket, outpointDataCorrect)
	}

	migtest.ApplyMigration(t, before, after, FixMigration20, false)
}
