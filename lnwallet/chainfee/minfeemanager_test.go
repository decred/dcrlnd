package chainfee

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mockChainBackend struct {
	minFee    AtomPerKByte
	callCount int
}

func (m *mockChainBackend) fetchFee() (AtomPerKByte, error) {
	m.callCount++
	return m.minFee, nil
}

// TestMinFeeManager tests that the minFeeManager returns an up to date min fee
// by querying the chain backend and that it returns a cached fee if the chain
// backend was recently queried.
func TestMinFeeManager(t *testing.T) {
	t.Parallel()

	chainBackend := &mockChainBackend{
		minFee: AtomPerKByte(10000),
	}

	// Initialise the min fee manager. This should call the chain backend
	// once.
	feeManager, err := newMinFeeManager(
		100*time.Millisecond,
		chainBackend.fetchFee,
	)
	require.NoError(t, err)
	require.Equal(t, 1, chainBackend.callCount)

	// If the fee is requested again, the stored fee should be returned
	// and the chain backend should not be queried.
	chainBackend.minFee = AtomPerKByte(20000)
	minFee := feeManager.fetchMinFee()
	require.Equal(t, minFee, AtomPerKByte(10000))
	require.Equal(t, 1, chainBackend.callCount)

	// Fake the passing of time.
	feeManager.lastUpdatedTime = time.Now().Add(-200 * time.Millisecond)

	// If the fee is queried again after the backoff period has passed
	// then the chain backend should be queried again for the min fee.
	minFee = feeManager.fetchMinFee()
	require.Equal(t, AtomPerKByte(20000), minFee)
	require.Equal(t, 2, chainBackend.callCount)
}
