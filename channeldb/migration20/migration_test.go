package migration20

import (
	"testing"

	"github.com/decred/dcrlnd/channeldb/kvdb"
	"github.com/decred/dcrlnd/channeldb/migtest"
)

var (
	hexStr = migtest.Hex

	nodeID      = hexStr("031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce")
	chainHash   = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce9")
	outpoint1   = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce90fb463ad")
	commitKey1  = hexStr("6368616e2d636f6d6d69746d656e742d6b657900")
	commitment1 = hexStr("0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000023280000000000000bb828996f0422464389000000000000138801000000010000000000000000000000000000000000000000000000000000000000000000ffffffff070431dc001b0162ffffffff0100f2052a01000000434104d64bdfd09eb1c5fe295abdeb1dca4281be988e2da0b6c1c6a59dc226c28624e18175e851c96b973d81b01cc31f047834bc06d6d6edf620d184241a6aed8b63a6ac0500000047010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010100014730450221008ce2bc69281ce27da07e6683571319d18e949ddfa2965fb6caa1bf0314f882d70220299105481d63e0f4bc2a88121167221b6700d72a0ead154c03be696a292d24ae81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce9000000000000000a000000010000000001096f6e696f6e626c6f6200000000000000000000000000000000")
	commitKey2  = hexStr("6368616e2d636f6d6d69746d656e742d6b657901")
	commitment2 = hexStr("000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000bb800000000000023286342a17ec61d66b3000000000000138801000000010000000000000000000000000000000000000000000000000000000000000000ffffffff070431dc001b0162ffffffff0100f2052a01000000434104d64bdfd09eb1c5fe295abdeb1dca4281be988e2da0b6c1c6a59dc226c28624e18175e851c96b973d81b01cc31f047834bc06d6d6edf620d184241a6aed8b63a6ac0500000047010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010100014730450221008ce2bc69281ce27da07e6683571319d18e949ddfa2965fb6caa1bf0314f882d70220299105481d63e0f4bc2a88121167221b6700d72a0ead154c03be696a292d24ae81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce9000000000000000a000000010000000000096f6e696f6e626c6f6200000000000000000000000000000000")
	chanInfo    = hexStr("1081b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce981b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce90fb463ad6fb5ae2162a7a98e01010000000064000400031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce00000000000027100000000000000008000000000000000201000000010000000000000000000000000000000000000000000000000000000000000000ffffffff070431dc001b0162ffffffff0100f2052a01000000434104d64bdfd09eb1c5fe295abdeb1dca4281be988e2da0b6c1c6a59dc226c28624e18175e851c96b973d81b01cc31f047834bc06d6d6edf620d184241a6aed8b63a6ac0500000041bc5221edd86a320d13a74909313ec348a942f50a648bb159bb2f3362169465c3a4f6d0000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce1eeeb7a89ae233c330b2e1eec7b8115c343083544c79ea954d38071b259e6703c8e5996c000000000000000901031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000010000000801031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000030000000701031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000040000000601031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000020000000501031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce")
	frozenInfo  = hexStr("00000064")
	revState    = hexStr("031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce9010000ffffffffffff537f9d8d5794fb14d5be766c16d3569466fd94186d89dbc48ce34909ef7a604d0000fffffffffffe031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce")

	// openChannel is the data in the open channel bucket in database
	// version 16 format.
	openChannel = map[string]interface{}{
		nodeID: map[string]interface{}{
			chainHash: map[string]interface{}{
				outpoint1: map[string]interface{}{
					commitKey1:             commitment1,
					commitKey2:             commitment2,
					"chan-info-key":        chanInfo,
					"frozen-chans":         frozenInfo,
					"revocation-state-key": revState,
				},
			},
		},
	}

	outpoint2     = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c7")
	outpoint2Info = hexStr("81b637d8fcd2c6da6859e6963113a1170de793e4b725b84d1e0b4cf99ec58ce952d6c6c700000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000000000000000001f40000000000002710000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce1a1e52008bd73aae77799e9e90cc5c154fbb11704005ce08717d115a04942e89aee57f81000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce000000000000000001031df11fcf521bf537579cd91c192b28343adcd04970817bcd68a3ca43648effce0102bcd89759e0688feed099ea7ed37ceb4fb808e4af814fe1986e3d30107aa3069f00")

	// closedChannel is the data in the closed channel bucket in database
	// version 16 format.
	closedChannel = map[string]interface{}{
		outpoint2: outpoint2Info,
	}

	// outpointData is used to create the outpoint bucket from scratch since
	// migration19 creates it.
	outpointData = map[string]interface{}{}

	// The tlv streams in string form for both an outpointOpen and outpointClosed
	// indexStatus.
	tlvOutpointOpen   = hexStr("000100")
	tlvOutpointClosed = hexStr("000101")

	// post is the expected data in the outpoint bucket after migration.
	post = map[string]interface{}{
		outpoint1: tlvOutpointOpen,
		outpoint2: tlvOutpointClosed,
	}
)

// TestMigrateOutpointIndex asserts that the database is properly migrated to
// contain an outpoint index.
func TestMigrateOutpointIndex(t *testing.T) {
	// Prime the database with both the openChannel and closedChannel data.
	// We also create the outpointBucket since migration19 creates it and
	// migration20 assumes it exists.
	before := func(tx kvdb.RwTx) error {
		err := migtest.RestoreDB(tx, openChanBucket, openChannel)
		if err != nil {
			return err
		}

		err = migtest.RestoreDB(tx, closedChannelBucket, closedChannel)
		if err != nil {
			return err
		}

		return migtest.RestoreDB(tx, outpointBucket, outpointData)
	}

	// Make sure that the openChanBucket and closedChannelBuckets are untouched
	// and that the outpoint index is populated correctly.
	after := func(tx kvdb.RwTx) error {
		err := migtest.VerifyDB(tx, openChanBucket, openChannel)
		if err != nil {
			return err
		}

		err = migtest.VerifyDB(tx, closedChannelBucket, closedChannel)
		if err != nil {
			return err
		}

		return migtest.VerifyDB(tx, outpointBucket, post)
	}

	migtest.ApplyMigration(t, before, after, MigrateOutpointIndex, false)
}
