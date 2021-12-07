package chainfee

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"
)

type mockSparseConfFeeSource struct {
	url  string
	fees map[uint32]uint32
}

func (e mockSparseConfFeeSource) GenQueryURL() string {
	return e.url
}

func (e mockSparseConfFeeSource) ParseResponse(r io.Reader) (map[uint32]uint32, error) {
	return e.fees, nil
}

type emptyReadCloser struct{}

func (e emptyReadCloser) Read(b []byte) (int, error) {
	return 0, nil
}
func (e emptyReadCloser) Close() error {
	return nil
}

func emptyGetter(url string) (*http.Response, error) {
	return &http.Response{
		Body: emptyReadCloser{},
	}, nil
}

// TestStaticFeeEstimator checks that the StaticFeeEstimator returns the
// expected fee rate.
func TestStaticFeeEstimator(t *testing.T) {
	t.Parallel()

	const feePerKb = FeePerKBFloor

	feeEstimator := NewStaticEstimator(feePerKb, 0)
	if err := feeEstimator.Start(); err != nil {
		t.Fatalf("unable to start fee estimator: %v", err)
	}
	defer feeEstimator.Stop()

	feeRate, err := feeEstimator.EstimateFeePerKB(6)
	if err != nil {
		t.Fatalf("unable to get fee rate: %v", err)
	}

	if feeRate != feePerKb {
		t.Fatalf("expected fee rate %v, got %v", feePerKb, feeRate)
	}
}

// TestSparseConfFeeSource checks that SparseConfFeeSource generates URLs and
// parses API responses as expected.
func TestSparseConfFeeSource(t *testing.T) {
	t.Parallel()

	// Test that GenQueryURL returns the URL as is.
	url := "test"
	feeSource := SparseConfFeeSource{URL: url}
	queryURL := feeSource.GenQueryURL()
	if queryURL != url {
		t.Fatalf("expected query URL of %v, got %v", url, queryURL)
	}

	// Test parsing a properly formatted JSON API response.
	// First, create the response as a bytes.Reader.
	testFees := map[uint32]uint32{
		1: 12345,
		2: 42,
		3: 54321,
	}
	testJSON := map[string]map[uint32]uint32{"fee_by_block_target": testFees}
	jsonResp, err := json.Marshal(testJSON)
	if err != nil {
		t.Fatalf("unable to marshal JSON API response: %v", err)
	}
	reader := bytes.NewReader(jsonResp)

	// Finally, ensure the expected map is returned without error.
	fees, err := feeSource.ParseResponse(reader)
	if err != nil {
		t.Fatalf("unable to parse API response: %v", err)
	}
	if !reflect.DeepEqual(fees, testFees) {
		t.Fatalf("expected %v, got %v", testFees, fees)
	}

	// Test parsing an improperly formatted JSON API response.
	badFees := map[string]uint32{"hi": 12345, "hello": 42, "satoshi": 54321}
	badJSON := map[string]map[string]uint32{"fee_by_block_target": badFees}
	jsonResp, err = json.Marshal(badJSON)
	if err != nil {
		t.Fatalf("unable to marshal JSON API response: %v", err)
	}
	reader = bytes.NewReader(jsonResp)

	// Finally, ensure the improperly formatted fees error.
	_, err = feeSource.ParseResponse(reader)
	if err == nil {
		t.Fatalf("expected ParseResponse to fail")
	}
}

// TestWebAPIFeeEstimator checks that the WebAPIFeeEstimator returns fee rates
// as expected.
func TestWebAPIFeeEstimator(t *testing.T) {
	t.Parallel()

	feeFloor := uint32(FeePerKBFloor)
	testCases := []struct {
		name   string
		target uint32
		apiEst uint32
		est    uint32
		err    string
	}{
		{"target_below_min", 0, 12345, 12345, "too low, minimum"},
		{"target_w_too-low_fee", 10, 42, feeFloor, ""},
		{"API-omitted_target", 2, 0, 0, "web API does not include"},
		{"valid_target", 20, 54321, 54321, ""},
		{"valid_target_extrapolated_fee", 25, 0, 54321, ""},
	}

	// Construct mock fee source for the Estimator to pull fees from.
	testFees := make(map[uint32]uint32)
	for _, tc := range testCases {
		if tc.apiEst != 0 {
			testFees[tc.target] = tc.apiEst
		}
	}

	spew.Dump(testFees)

	feeSource := mockSparseConfFeeSource{
		url:  "https://www.github.com",
		fees: testFees,
	}

	estimator := NewWebAPIEstimator(feeSource, false)
	estimator.netGetter = emptyGetter

	// Test that requesting a fee when no fees have been cached fails.
	_, err := estimator.EstimateFeePerKB(5)
	if err == nil ||
		!strings.Contains(err.Error(), "web API does not include") {

		t.Fatalf("expected fee estimation to fail, instead got: %v", err)
	}

	if err := estimator.Start(); err != nil {
		t.Fatalf("unable to start fee estimator, got: %v", err)
	}
	defer estimator.Stop()

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			est, err := estimator.EstimateFeePerKB(tc.target)
			if tc.err != "" {
				if err == nil ||
					!strings.Contains(err.Error(), tc.err) {

					t.Fatalf("expected fee estimation to "+
						"fail, instead got: %v", err)
				}
			} else {
				exp := AtomPerKByte(tc.est)
				if err != nil {
					t.Fatalf("unable to estimate fee for "+
						"%v block target, got: %v",
						tc.target, err)
				}
				if est != exp {
					t.Fatalf("expected fee estimate of "+
						"%v, got %v", exp, est)
				}
			}
		})
	}
}

// TestGetCachedFee checks that the fee caching logic works as expected.
func TestGetCachedFee(t *testing.T) {
	target := uint32(2)
	fee := uint32(100)

	// Create a dummy estimator without WebAPIFeeSource.
	estimator := NewWebAPIEstimator(nil, false)

	// When the cache is empty, an error should be returned.
	cachedFee, err := estimator.getCachedFee(target)
	require.Zero(t, cachedFee)
	require.ErrorIs(t, err, errEmptyCache)

	// Store a fee rate inside the cache.
	estimator.feeByBlockTarget[target] = fee

	testCases := []struct {
		name        string
		confTarget  uint32
		expectedFee uint32
		expectErr   error
	}{
		{
			// When the target is cached, return it.
			name:        "return cached fee",
			confTarget:  target,
			expectedFee: fee,
			expectErr:   nil,
		},
		{
			// When the target is not cached, return the next
			// lowest target that's cached.
			name:        "return next cached fee",
			confTarget:  target + 1,
			expectedFee: fee,
			expectErr:   nil,
		},
		{
			// When the target is not cached, and the next lowest
			// target is not cached, return the nearest fee rate.
			name:        "return highest cached fee",
			confTarget:  target - 1,
			expectedFee: fee,
			expectErr:   nil,
		},
	}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			cachedFee, err := estimator.getCachedFee(tc.confTarget)

			require.Equal(t, tc.expectedFee, cachedFee)
			require.ErrorIs(t, err, tc.expectErr)
		})
	}

}
