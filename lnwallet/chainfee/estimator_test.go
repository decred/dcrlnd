package chainfee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
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
		{"target_below_min", 1, 12345, 12345, "too low, minimum"},
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

	estimator := NewWebAPIEstimator(feeSource, 10)
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

func TestDCRDataFeeEstimator(t *testing.T) {
	const fallbackRate = 123
	const numBlocks = 5

	est := NewDCRDataEstimator(false, fallbackRate)

	// Test the success path just once, since this will generate a network
	// request. Don't fail on error.
	_, err := est.EstimateFeePerKB(2)
	if err != nil {
		log.Warnf("error getting live fee rate: %v", err)
	}

	// Start with a fresh cache.
	est = NewDCRDataEstimator(false, fallbackRate)

	requested := false
	var currentEstimate AtomPerKByte = 321
	var currentErr error

	est.fetchRate = func(_ context.Context, _ string, numBlocks uint32) (AtomPerKByte, error) {
		requested = true
		return currentEstimate, currentErr
	}

	checkResults := func(expRate AtomPerKByte, expRequest bool) {
		t.Helper()
		requested = false
		feeRate, err := est.EstimateFeePerKB(numBlocks)
		if err != nil {
			t.Fatalf("EstimateFeePerKB error: %v", err)
		}
		if feeRate != expRate {
			t.Fatalf("expected fee rate %d, got %d", expRate, feeRate)
		}
		if requested != expRequest {
			t.Fatalf("expected request = %t, got %t", expRequest, requested)
		}
	}

	// First query will generate a request.
	checkResults(currentEstimate, true)

	// The next request for the same one should not generate a request.
	checkResults(currentEstimate, false)

	// Now expire the cache.
	est.cache[numBlocks].stamp = time.Now().Add(-dcrdataFeeExpiration)
	currentEstimate++
	checkResults(currentEstimate, true)

	// Expired cache + request error is not propagated.
	est.cache[numBlocks].stamp = time.Now().Add(-dcrdataFeeExpiration)
	currentErr = fmt.Errorf("test error")
	checkResults(fallbackRate, true)

	// Clearing the error won't result in a request until the failure is
	// expired.
	currentErr = nil
	checkResults(fallbackRate, false)

	// Failures expire too.
	est.lastFail = time.Now().Add(-dcrdataFailExpiration)
	currentEstimate++
	checkResults(currentEstimate, true)
}
