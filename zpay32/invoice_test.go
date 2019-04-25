package zpay32

// We use package `zpay32` rather than `zpay32_test` in order to share test data
// with the internal tests.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/routing"
)

var (
	testMilliAt24DCR    = lnwire.MilliAtom(2400000000000)
	testMilliAt2500uDCR = lnwire.MilliAtom(250000000)
	testMilliAt20mDCR   = lnwire.MilliAtom(2000000000)

	testPaymentHashSlice, _ = hex.DecodeString("0001020304050607080900010203040506070809000102030405060708090102")

	testEmptyString    = ""
	testCupOfCoffee    = "1 cup coffee"
	testCupOfNonsense  = "ナンセンス 1杯"
	testPleaseConsider = "Please consider supporting this project"

	testPrivKeyBytes, _     = hex.DecodeString("e126f68f7eafcc8b74f54d269fe206be715000f94dac067d1c04a8ca3b2db734")
	testPrivKey, testPubKey = secp256k1.PrivKeyFromBytes(testPrivKeyBytes)

	testDescriptionHashSlice = chainhash.HashB([]byte("One piece of chocolate cake, one icecream cone, one pickle, one slice of swiss cheese, one slice of salami, one lollypop, one piece of cherry pie, one sausage, one cupcake, and one slice of watermelon"))

	testExpiry0  = time.Duration(0) * time.Second
	testExpiry60 = time.Duration(60) * time.Second

	testAddrTestnet, _     = dcrutil.DecodeAddress("TsR28UZRprhgQQhzWns2M6cAwchrNVvbYq2")
	testRustyAddr, _       = dcrutil.DecodeAddress("DsQxuVRvS4eaJ42dhQEsCXauMWjvopWgrVg")
	testAddrMainnetP2SH, _ = dcrutil.DecodeAddress("DcXTb4QtmnyRsnzUVViYQawqFE5PuYTdX2C")

	testHopHintPubkeyBytes1, _ = hex.DecodeString("029e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255")
	testHopHintPubkey1, _      = secp256k1.ParsePubKey(testHopHintPubkeyBytes1)
	testHopHintPubkeyBytes2, _ = hex.DecodeString("039e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255")
	testHopHintPubkey2, _      = secp256k1.ParsePubKey(testHopHintPubkeyBytes2)

	testSingleHop = []routing.HopHint{
		{
			NodeID:                    testHopHintPubkey1,
			ChannelID:                 0x0102030405060708,
			FeeBaseMAtoms:             0,
			FeeProportionalMillionths: 20,
			CLTVExpiryDelta:           3,
		},
	}
	testDoubleHop = []routing.HopHint{
		{
			NodeID:                    testHopHintPubkey1,
			ChannelID:                 0x0102030405060708,
			FeeBaseMAtoms:             1,
			FeeProportionalMillionths: 20,
			CLTVExpiryDelta:           3,
		},
		{
			NodeID:                    testHopHintPubkey2,
			ChannelID:                 0x030405060708090a,
			FeeBaseMAtoms:             2,
			FeeProportionalMillionths: 30,
			CLTVExpiryDelta:           4,
		},
	}

	testMessageSigner = MessageSigner{
		SignCompact: func(hash []byte) ([]byte, error) {
			sig, err := secp256k1.SignCompact(testPrivKey, hash, true)
			if err != nil {
				return nil, fmt.Errorf("can't sign the "+
					"message: %v", err)
			}
			return sig, nil
		},
	}

	// Must be initialized in init().
	testPaymentHash     [32]byte
	testDescriptionHash [32]byte
)

func init() {
	copy(testPaymentHash[:], testPaymentHashSlice)
	copy(testDescriptionHash[:], testDescriptionHashSlice)
}

// TestDecodeEncode tests that an encoded invoice gets decoded into the expected
// Invoice object, and that reencoding the decoded invoice gets us back to the
// original encoded string.
func TestDecodeEncode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		encodedInvoice string
		valid          bool
		decodedInvoice func() *Invoice
		skipEncoding   bool
		beforeEncoding func(*Invoice)
	}{
		{
			encodedInvoice: "asdsaddnasdnas", // no hrp
			valid:          false,
		},
		{
			encodedInvoice: "lndcr1abcde", // too short
			valid:          false,
		},
		{
			encodedInvoice: "1asdsaddnv4wudz", // empty hrp
			valid:          false,
		},
		{
			encodedInvoice: "ln1asdsaddnv4wudz", // hrp too short
			valid:          false,
		},
		{
			encodedInvoice: "llts1dasdajtkfl6", // no "ln" prefix
			valid:          false,
		},
		{
			encodedInvoice: "lnts1dasdapukz0w", // invalid segwit prefix
			valid:          false,
		},
		{
			encodedInvoice: "lndcrm1aaamcu25m", // invalid amount
			valid:          false,
		},
		{
			encodedInvoice: "lndcr1000000000m1", // invalid amount
			valid:          false,
		},
		{
			encodedInvoice: "lndcr20m1pvjluezhp58yjmdan79s6qqdhdzgynm4zwqd5d7xmw5fk98klysy043l2ahrqspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqfqqepvrhrm9s57hejg0p662ur5j5cr03890fa7k2pypgttmh4897d3raaq85a293e9jpuqwl0rnfuwzam7yr8e690nd2ypcq9hlkdwdvycqjhlqg5", // empty fallback address field
			valid:          false,
		},
		{
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp58yjmdan79s6qqdhdzgynm4zwqd5d7xmw5fk98klysy043l2ahrqsfpp3qjmp7lwpagxun9pygexvgpjdc4jdj85frqg00000000j9n4evl6mr5aj9f58zp6fyjzup6ywn3x6sk8akg5v4tgn2q8g4fhx05wf6juaxu9760yp46454gpg5mtzgerlzezqcqvjnhjh8z3g2qqsj5cgu", // invalid routing info length: not a multiple of 51
			valid:          false,
		},
		{
			// no payment hash set
			encodedInvoice: "lndcr20m1pvjluezhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsqdnpkqmwa5g78xlhq029a2238kjf9klaes6pc6qvgasvvz729r0zjurxzvj5ssr2ypjs9af6qdfhkdrwwf0urkhh8p8lx4p65jq0tkcpfdajj0",
			valid:          false,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
				}
			},
		},
		{
			// Both Description and DescriptionHash set.
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6twvus8g6rfwvs8qun0dfjkxaqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hs0pwljxh5kezjcylatfknd2wgpc5kyayf8wntsjsaxhyw3cw0a6s9ue5y4lkeja470cldvwx075d2s06acaphjsnc4mq74nzcu0lcr0qqkady8f",
			valid:          false,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					Description:     &testPleaseConsider,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
				}
			},
		},
		{
			// Neither Description nor DescriptionHash set.
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypq8puwce8w28d8elye424a7y7l845llxkwtku8sjpf2c42ltqd7vehefdhpr0zgq4dxjl36savrdn0perhuu2n9dxhd30suv24rltkdtcpgx6lc2",
			valid:          false,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.MainNetParams,
					MilliAt:     &testMilliAt20mDCR,
					Timestamp:   time.Unix(1496314658, 0),
					PaymentHash: &testPaymentHash,
					Destination: testPubKey,
				}
			},
		},
		{
			// Has a few unknown fields, should just be ignored.
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6twvus8g6rfwvs8qun0dfjkxaqtq2v93xxer9vczq8v93xxeqy2e8qjln3aelvnu077437ta5l3c6eq2ag7ervsa6l24kaanjgusy8984984ykpv73jent2c2c0zfj6sjrfraz52dq4f77hup0azr0cgpak5p5f",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.MainNetParams,
					MilliAt:     &testMilliAt20mDCR,
					Timestamp:   time.Unix(1496314658, 0),
					PaymentHash: &testPaymentHash,
					Description: &testPleaseConsider,
					Destination: testPubKey,
				}
			},
			skipEncoding: true, // Skip encoding since we don't have the unknown fields to encode.
		},
		{
			// Ignore unknown witness version in fallback address.
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfpzpw508d6qejxtdg4y5r3zarvary0c5xw7kqt00sjdsfuzdw87n0rlvqw0h6ul6m66s38cr3wfh3epe7e3qn8e88wjdu2n39aalhnxtpx8faczr8g4uhktkkedms76qcs0tuqaju2egppmk8nq",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
				}
			},
			skipEncoding: true, // Skip encoding since we don't have the unknown fields to encode.
		},
		{
			// Ignore fields with unknown lengths.
			encodedInvoice: "lndcr241pveeq09pp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqpp3qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hshp38yjmdan79s6qqdhdzgynm4zwqd5d7xmw5fk98klysy043l2ahnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66np3q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfkq0wl2r2kwnhqc7mm8wmseekc2wj0rsj9nssxprqwerqfgx9lau5samrfeenge47pfjlectk4yj9axr42jpcl53e43wdlxln6amlmmcpat04z9",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt24DCR,
					Timestamp:       time.Unix(1503429093, 0),
					PaymentHash:     &testPaymentHash,
					Destination:     testPubKey,
					DescriptionHash: &testDescriptionHash,
				}
			},
			skipEncoding: true, // Skip encoding since we don't have the unknown fields to encode.
		},
		{
			// Invoice with no amount.
			encodedInvoice: "lndcr1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsmvp0ygkvzd3zh9wkfj59cuze0se5fzuh4f7rysdukv68n6fafa45sudrzg8d33paaw50zczd5mzmppqaalvzneu0yd3zfrvzhnfzpkgppyrza2",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.MainNetParams,
					Timestamp:   time.Unix(1496314658, 0),
					PaymentHash: &testPaymentHash,
					Description: &testCupOfCoffee,
					Destination: testPubKey,
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// Please make a donation of any amount using rhash 0001020304050607080900010203040506070809000102030405060708090102 to me @03e7156ae33b0a208d0744199163177e909e80176e55d97a2f221ede0f934dd9ad
			encodedInvoice: "lndcr1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6twvus8g6rfwvs8qun0dfjkxaq708hahqy65t7nzxer9t26yxkxxmh84rc7u7hfv2wxrjjkf5v8wqz3tf472p8dagx7u2fayqzfp8ycekwmfmacz5g83tunacfzw48m0sq4jcr74",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.MainNetParams,
					Timestamp:   time.Unix(1496314658, 0),
					PaymentHash: &testPaymentHash,
					Description: &testPleaseConsider,
					Destination: testPubKey,
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// Same as above, pubkey set in 'n' field.
			encodedInvoice: "lndcr241pveeq09pp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdqqnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66husxpmqj9fh878hrkccqzvazqk2mhj0fdtjyngvhz5vje86eh39zu8cmp7k0kml38p3d3ujyuuhqe32kfgdt98t5e8r74xmwk53u5mqqm45579",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.MainNetParams,
					MilliAt:     &testMilliAt24DCR,
					Timestamp:   time.Unix(1503429093, 0),
					PaymentHash: &testPaymentHash,
					Destination: testPubKey,
					Description: &testEmptyString,
				}
			},
		},
		{
			// Please send $3 for a cup of coffee to the same peer, within 1 minute
			encodedInvoice: "lndcr2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpumkqh4xxuwhghf0dy5mglqnnttyg46a3ursmwv33dlwvmvkt9d8z9k7h4nhm0uun3a8hly8e92hd926j0tm0afrnzqeyapnlqhrx6cugphffhw0",
			valid:          true,
			decodedInvoice: func() *Invoice {
				i, _ := NewInvoice(
					&chaincfg.MainNetParams,
					testPaymentHash,
					time.Unix(1496314658, 0),
					Amount(testMilliAt2500uDCR),
					Description(testCupOfCoffee),
					Destination(testPubKey),
					Expiry(testExpiry60))
				return i
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// Please send 0.0025 DCR for a cup of nonsense (ナンセンス 1杯) to the same peer, within 1 minute
			encodedInvoice: "lndcr2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpquwpc4curk03c9wlrswe78q4eyqc7d8d0xqzpu20gghk3mf6c570cuquvdxd0p0wym6pdcmcdmpnnhjs557vvxzvprplpwrzxef7a6emdtkv5vjeqg5mk4ea55u4wt0qk6pp0gc67w4zgq7xl5pn",
			valid:          true,
			decodedInvoice: func() *Invoice {
				i, _ := NewInvoice(
					&chaincfg.MainNetParams,
					testPaymentHash,
					time.Unix(1496314658, 0),
					Amount(testMilliAt2500uDCR),
					Description(testCupOfNonsense),
					Destination(testPubKey),
					Expiry(testExpiry60))
				return i
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// Now send $24 for an entire list of things (hashed)
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hs3uzs6up5wjzjy7pl352h0rd0tujcwrvej3035gs59x2funkpx44z7r3ku04xf8xgvlxrc4dhaut5t9yxvwv2kvdge6g25zk6p87550qp0c2rnh",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// The same, on testnet, with a fallback address TsR28UZRprhgQQhzWns2M6cAwchrNVvbYq2
			encodedInvoice: "lntdcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfpp3qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpf094cd5v782dw6hu4uu2nadncanzy8emn3xzmp77n0nnzyrwzzxuel7sqyzgmrpvl4p3hncrztujznemavdwy38sa9wdmlrnzcdlscqytjln6",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.TestNet3Params,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
					FallbackAddr:    testAddrTestnet,
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// On mainnet, with fallback address DsQxuVRvS4eaJ42dhQEsCXauMWjvopWgrVg with extra routing info to get to node 029e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfpp3qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqrzjq20q82gphp2nflc7jtzrcazrra7wwgzxqc8u7754cdlpfrmccae92qgzqvzq2ps8pqqqqqqqqqqqq9qqqvchhyegdla6jsqjquef6f7k9m7gfj3kze2rmqphv24tcsr0v3geqk6w4mgzmup6040rvy9gy0jxlwwvqfv2ua0ycggkammcquq57y4wcqpyayvm",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
					FallbackAddr:    testRustyAddr,
					RouteHints:      [][]routing.HopHint{testSingleHop},
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// On mainnet, with fallback address DsQxuVRvS4eaJ42dhQEsCXauMWjvopWgrVg with extra routing info to go via nodes 029e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255 then 039e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfpp3qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqr9yq20q82gphp2nflc7jtzrcazrra7wwgzxqc8u7754cdlpfrmccae92qgzqvzq2ps8pqqqqqqpqqqqq9qqqvpeuqafqxu92d8lr6fvg0r5gv0heeeqgcrqlnm6jhphu9y00rrhy4grqszsvpcgpy9qqqqqqgqqqqq7qqzqykl3fr9qy3yxam6xh55lxtfcp7uxsdl4krv6206de6j4lvfdu0l4hjwsy9aad8ap527ygzpc0gcrx8t98gxn3kr2xaq2nympn0jv9rqpqjas5d",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
					FallbackAddr:    testRustyAddr,
					RouteHints:      [][]routing.HopHint{testDoubleHop},
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// On mainnet, with fallback (p2sh) address DcXTb4QtmnyRsnzUVViYQawqFE5PuYTdX2C
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfppjqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq3wf2zppfn42k4t02y02rrlqner5zkg90qhy9km6m7pkd2euet2dsuqxr4qjgwns45pjhrc4vauau0f05576du50ahs87c7pvt4hm2mcq3gfmam",
			valid:          true,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:             &chaincfg.MainNetParams,
					MilliAt:         &testMilliAt20mDCR,
					Timestamp:       time.Unix(1496314658, 0),
					PaymentHash:     &testPaymentHash,
					DescriptionHash: &testDescriptionHash,
					Destination:     testPubKey,
					FallbackAddr:    testAddrMainnetP2SH,
				}
			},
			beforeEncoding: func(i *Invoice) {
				// Since this destination pubkey was recovered
				// from the signature, we must set it nil before
				// encoding to get back the same invoice string.
				i.Destination = nil
			},
		},
		{
			// Send 2500uDCR for a cup of coffee with a custom CLTV
			// expiry value.
			encodedInvoice: "lndcr2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jscqzysnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66fxyjhqjk2p5tfgzh8ddfkc7s25nc0qefzt86v7ct6rccv6577f753juhjra927ma6cg05wpd6a2tqjgc56ttp5xygrk237apy5e5vgqq660qqy",
			valid:          true,
			decodedInvoice: func() *Invoice {
				i, _ := NewInvoice(
					&chaincfg.MainNetParams,
					testPaymentHash,
					time.Unix(1496314658, 0),
					Amount(testMilliAt2500uDCR),
					Description(testCupOfCoffee),
					Destination(testPubKey),
					CLTVExpiry(144),
				)

				return i
			},
		},
		{
			// Decode a mainnet invoice while expecting active net to be testnet
			encodedInvoice: "lndcr241pveeq09pp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdqqnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66husxpmqj9fh878hrkccqzvazqk2mhj0fdtjyngvhz5vje86eh39zu8cmp7k0kml38p3d3ujyuuhqe32kfgdt98t5e8r74xmwk53u5mqqm45579",
			valid:          false,
			decodedInvoice: func() *Invoice {
				return &Invoice{
					Net:         &chaincfg.TestNet3Params,
					MilliAt:     &testMilliAt24DCR,
					Timestamp:   time.Unix(1503429093, 0),
					PaymentHash: &testPaymentHash,
					Destination: testPubKey,
					Description: &testEmptyString,
				}
			},
			skipEncoding: true, // Skip encoding since we were given the wrong net
		},
	}

	for i, test := range tests {
		var decodedInvoice *Invoice
		net := &chaincfg.MainNetParams
		if test.decodedInvoice != nil {
			decodedInvoice = test.decodedInvoice()
			net = decodedInvoice.Net
		}

		invoice, err := Decode(test.encodedInvoice, net)
		if (err == nil) != test.valid {
			t.Errorf("Decoding test %d failed: %v", i, err)
			return
		}

		if test.valid {
			if err := compareInvoices(test.decodedInvoice(), invoice); err != nil {
				t.Errorf("Invoice decoding result %d not as expected: %v", i, err)
				return
			}
		}

		if test.skipEncoding {
			continue
		}

		if test.beforeEncoding != nil {
			test.beforeEncoding(decodedInvoice)
		}

		if decodedInvoice != nil {
			reencoded, err := decodedInvoice.Encode(
				testMessageSigner,
			)
			if (err == nil) != test.valid {
				t.Errorf("Encoding test %d failed: %v", i, err)
				return
			}

			if test.valid && test.encodedInvoice != reencoded {
				t.Errorf("Encoding %d failed, expected %v, got %v",
					i, test.encodedInvoice, reencoded)
				return
			}
		}
	}
}

// TestNewInvoice tests that providing the optional arguments to the NewInvoice
// method creates an Invoice that encodes to the expected string.
func TestNewInvoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		newInvoice     func() (*Invoice, error)
		encodedInvoice string
		valid          bool
	}{
		{
			// Both Description and DescriptionHash set.
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(&chaincfg.MainNetParams,
					testPaymentHash, time.Unix(1496314658, 0),
					DescriptionHash(testDescriptionHash),
					Description(testPleaseConsider))
			},
			valid: false, // Both Description and DescriptionHash set.
		},
		{
			// Invoice with no amount.
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(
					&chaincfg.MainNetParams,
					testPaymentHash,
					time.Unix(1496314658, 0),
					Description(testCupOfCoffee),
				)
			},
			valid:          true,
			encodedInvoice: "lndcr1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsmvp0ygkvzd3zh9wkfj59cuze0se5fzuh4f7rysdukv68n6fafa45sudrzg8d33paaw50zczd5mzmppqaalvzneu0yd3zfrvzhnfzpkgppyrza2",
		},
		{
			// 'n' field set.
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(&chaincfg.MainNetParams,
					testPaymentHash, time.Unix(1503429093, 0),
					Amount(testMilliAt24DCR),
					Description(testEmptyString),
					Destination(testPubKey))
			},
			valid:          true,
			encodedInvoice: "lndcr241pveeq09pp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdqqnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66husxpmqj9fh878hrkccqzvazqk2mhj0fdtjyngvhz5vje86eh39zu8cmp7k0kml38p3d3ujyuuhqe32kfgdt98t5e8r74xmwk53u5mqqm45579",
		},
		{
			// On mainnet, with fallback address DsQxuVRvS4eaJ42dhQEsCXauMWjvopWgrVg with extra routing info to go via nodes 029e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255 then 039e03a901b85534ff1e92c43c74431f7ce72046060fcf7a95c37e148f78c77255
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(&chaincfg.MainNetParams,
					testPaymentHash, time.Unix(1496314658, 0),
					Amount(testMilliAt20mDCR),
					DescriptionHash(testDescriptionHash),
					FallbackAddr(testRustyAddr),
					RouteHint(testDoubleHop),
				)
			},
			valid:          true,
			encodedInvoice: "lndcr20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp5p0y6smqsu95wrj2v9dzntwn88pmz4ck92063nkhxju832w0tr5hsfpp3qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqr9yq20q82gphp2nflc7jtzrcazrra7wwgzxqc8u7754cdlpfrmccae92qgzqvzq2ps8pqqqqqqpqqqqq9qqqvpeuqafqxu92d8lr6fvg0r5gv0heeeqgcrqlnm6jhphu9y00rrhy4grqszsvpcgpy9qqqqqqgqqqqq7qqzqykl3fr9qy3yxam6xh55lxtfcp7uxsdl4krv6206de6j4lvfdu0l4hjwsy9aad8ap527ygzpc0gcrx8t98gxn3kr2xaq2nympn0jv9rqpqjas5d",
		},
		{
			// On simnet
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(&chaincfg.SimNetParams,
					testPaymentHash, time.Unix(1496314658, 0),
					Amount(testMilliAt24DCR),
					Description(testEmptyString),
					Destination(testPubKey))
			},
			valid:          true,
			encodedInvoice: "lnsdcr241pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdqqnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv66zh5xhvtchse36pt88lj4djy8g58lx26xfz3np7humcd9594rmgv92nws6vllf9mhq670x9nrwhjzw0shsklr6gq235whh9x9089ue7gpjur6cc",
		},
		{
			// On regtest
			newInvoice: func() (*Invoice, error) {
				return NewInvoice(&chaincfg.RegNetParams,
					testPaymentHash, time.Unix(1496314658, 0),
					Amount(testMilliAt24DCR),
					Description(testEmptyString),
					Destination(testPubKey))
			},
			valid:          true,
			encodedInvoice: "lnrdcr241pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdqqnp4q0n326hr8v9zprg8gsvezcch06gfaqqhde2aj730yg0durunfhv668eyx3hxz79l45wdm93chts7yvd7n5dd4peq0dwdkrdamnrylws34pynkyyw7dndfy047tcelp4l8w26j8jjht8urq204g3ca6tgm7ycpq5qkd2",
		},
	}

	for i, test := range tests {

		invoice, err := test.newInvoice()
		if err != nil && !test.valid {
			continue
		}
		encoded, err := invoice.Encode(testMessageSigner)
		if (err == nil) != test.valid {
			t.Errorf("NewInvoice test %d failed: %v", i, err)
			return
		}

		if test.valid && test.encodedInvoice != encoded {
			t.Errorf("Encoding %d failed, expected %v, got %v",
				i, test.encodedInvoice, encoded)
			return
		}
	}
}

func compareInvoices(expected, actual *Invoice) error {
	if !reflect.DeepEqual(expected.Net, actual.Net) {
		return fmt.Errorf("expected net %v, got %v",
			expected.Net, actual.Net)
	}

	if !reflect.DeepEqual(expected.MilliAt, actual.MilliAt) {
		return fmt.Errorf("expected milli atoms %d, got %d",
			*expected.MilliAt, *actual.MilliAt)
	}

	if expected.Timestamp != actual.Timestamp {
		return fmt.Errorf("expected timestamp %v, got %v",
			expected.Timestamp, actual.Timestamp)
	}

	if !compareHashes(expected.PaymentHash, actual.PaymentHash) {
		return fmt.Errorf("expected payment hash %x, got %x",
			*expected.PaymentHash, *actual.PaymentHash)
	}

	if !reflect.DeepEqual(expected.Description, actual.Description) {
		return fmt.Errorf("expected description \"%s\", got \"%s\"",
			*expected.Description, *actual.Description)
	}

	if !comparePubkeys(expected.Destination, actual.Destination) {
		return fmt.Errorf("expected destination pubkey %x, got %x",
			expected.Destination, actual.Destination)
	}

	if !compareHashes(expected.DescriptionHash, actual.DescriptionHash) {
		return fmt.Errorf("expected description hash %x, got %x",
			*expected.DescriptionHash, *actual.DescriptionHash)
	}

	if expected.Expiry() != actual.Expiry() {
		return fmt.Errorf("expected expiry %d, got %d",
			expected.Expiry(), actual.Expiry())
	}

	if !reflect.DeepEqual(expected.FallbackAddr, actual.FallbackAddr) {
		return fmt.Errorf("expected FallbackAddr %v, got %v",
			expected.FallbackAddr, actual.FallbackAddr)
	}

	if len(expected.RouteHints) != len(actual.RouteHints) {
		return fmt.Errorf("expected %d RouteHints, got %d",
			len(expected.RouteHints), len(actual.RouteHints))
	}

	for i, routeHint := range expected.RouteHints {
		err := compareRouteHints(routeHint, actual.RouteHints[i])
		if err != nil {
			return err
		}
	}

	return nil
}

func comparePubkeys(a, b *secp256k1.PublicKey) bool {
	if a == b {
		return true
	}
	if a == nil && b != nil {
		return false
	}
	if b == nil && a != nil {
		return false
	}
	return a.IsEqual(b)
}

func compareHashes(a, b *[32]byte) bool {
	if a == b {
		return true
	}
	if a == nil && b != nil {
		return false
	}
	if b == nil && a != nil {
		return false
	}
	return bytes.Equal(a[:], b[:])
}

func compareRouteHints(a, b []routing.HopHint) error {
	if len(a) != len(b) {
		return fmt.Errorf("expected len routingInfo %d, got %d",
			len(a), len(b))
	}

	for i := 0; i < len(a); i++ {
		if !comparePubkeys(a[i].NodeID, b[i].NodeID) {
			return fmt.Errorf("expected routeHint nodeID %x, "+
				"got %x", a[i].NodeID, b[i].NodeID)
		}

		if a[i].ChannelID != b[i].ChannelID {
			return fmt.Errorf("expected routeHint channelID "+
				"%d, got %d", a[i].ChannelID, b[i].ChannelID)
		}

		if a[i].FeeBaseMAtoms != b[i].FeeBaseMAtoms {
			return fmt.Errorf("expected routeHint feeBaseMAtoms %d, got %d",
				a[i].FeeBaseMAtoms, b[i].FeeBaseMAtoms)
		}

		if a[i].FeeProportionalMillionths != b[i].FeeProportionalMillionths {
			return fmt.Errorf("expected routeHint feeProportionalMillionths %d, got %d",
				a[i].FeeProportionalMillionths, b[i].FeeProportionalMillionths)
		}

		if a[i].CLTVExpiryDelta != b[i].CLTVExpiryDelta {
			return fmt.Errorf("expected routeHint cltvExpiryDelta "+
				"%d, got %d", a[i].CLTVExpiryDelta, b[i].CLTVExpiryDelta)
		}
	}

	return nil
}
