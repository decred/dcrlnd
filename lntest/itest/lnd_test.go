package itest

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
)

const (
	// defaultSplitTranches is the default number of tranches we split the
	// test cases into.
	defaultSplitTranches uint = 1

	// defaultRunTranche is the default index of the test cases tranche that
	// we run.
	defaultRunTranche uint = 0
)

var (
	// testCasesSplitParts is the number of tranches the test cases should
	// be split into. By default this is set to 1, so no splitting happens.
	// If this value is increased, then the -runtranche flag must be
	// specified as well to indicate which part should be run in the current
	// invocation.
	testCasesSplitTranches = flag.Uint(
		"splittranches", defaultSplitTranches, "split the test cases "+
			"in this many tranches and run the tranche at "+
			"0-based index specified by the -runtranche flag",
	)

	// testCasesRunTranche is the 0-based index of the split test cases
	// tranche to run in the current invocation.
	testCasesRunTranche = flag.Uint(
		"runtranche", defaultRunTranche, "run the tranche of the "+
			"split test cases with the given (0-based) index",
	)

	// dbBackendFlag specifies the backend to use
	dbBackendFlag = flag.String("dbbackend", "bbolt", "Database backend "+
		"(bbolt, etcd, postgres)")
)

// getTestCaseSplitTranche returns the sub slice of the test cases that should
// be run as the current split tranche as well as the index and slice offset of
// the tranche.
func getTestCaseSplitTranche() ([]*testCase, uint, uint) {
	numTranches := defaultSplitTranches
	if testCasesSplitTranches != nil {
		numTranches = *testCasesSplitTranches
	}
	runTranche := defaultRunTranche
	if testCasesRunTranche != nil {
		runTranche = *testCasesRunTranche
	}

	// There's a special flake-hunt mode where we run the same test multiple
	// times in parallel. In that case the tranche index is equal to the
	// thread ID, but we need to actually run all tests for the regex
	// selection to work.
	threadID := runTranche
	if numTranches == 1 {
		runTranche = 0
	}

	numCases := uint(len(allTestCases))
	testsPerTranche := numCases / numTranches
	trancheOffset := runTranche * testsPerTranche
	trancheEnd := trancheOffset + testsPerTranche
	if trancheEnd > numCases || runTranche == numTranches-1 {
		trancheEnd = numCases
	}

	return allTestCases[trancheOffset:trancheEnd], threadID, trancheOffset
}

// TestLightningNetworkDaemon performs a series of integration tests amongst a
// programmatically driven network of lnd nodes.
func TestLightningNetworkDaemon(t *testing.T) {
	// If no tests are registered, then we can exit early.
	if len(allTestCases) == 0 {
		t.Skip("integration tests not selected with flag 'rpctest'")
	}

	// Parse testing flags that influence our test execution.
	logDir := lntest.GetLogDir()
	require.NoError(t, os.MkdirAll(logDir, 0700))
	testCases, trancheIndex, trancheOffset := getTestCaseSplitTranche()
	lntest.ApplyPortOffset(uint32(trancheIndex) * 1000)

	ht := newHarnessTest(t, nil)

	// Declare the network harness here to gain access to its
	// 'OnTxAccepted' call back.
	var lndHarness *lntest.NetworkHarness

	// Create an instance of dcrd's rpctest.Harness that will act as the
	// miner for all tests. This will be used to fund the wallets of the
	// nodes within the test network and to drive blockchain related events
	// within the network. Revert the default setting of accepting
	// non-standard transactions on simnet to reject them. Transactions on
	// the lightning network should always be standard to get better
	// guarantees of getting included in to blocks.
	//
	// We will also connect it to our chain backend.
	miner, err := lntest.NewMiner()
	require.NoError(t, err, "failed to create new miner")
	defer func() {
		require.NoError(t, miner.Stop(), "failed to stop miner")
	}()

	// Setup the initial chain. This is a dcrlnd-only operation, needed to
	// setup the initial chain + the voting wallet.
	err = miner.SetUpChain()
	require.NoError(t, err)

	if err := miner.Node.NotifyNewTransactions(context.Background(), false); err != nil {
		ht.Fatalf("unable to request transaction notifications: %v", err)
	}

	// Start a chain backend.
	chainBackend, cleanUp, err := lntest.NewBackend(t, miner.Harness)
	if err != nil {
		ht.Fatalf("unable to start dcrd: %v", err)
	}
	defer func() {
		require.NoError(
			t, cleanUp(), "failed to clean up chain backend",
		)
	}()

	// Connect chainbackend to miner.
	require.NoError(
		t, chainBackend.ConnectMiner(), "failed to connect to miner",
	)

	// Parse database backend
	var dbBackend lntest.DatabaseBackend
	switch *dbBackendFlag {
	case "bbolt":
		dbBackend = lntest.BackendBbolt

	case "etcd":
		dbBackend = lntest.BackendEtcd

	case "postgres":
		dbBackend = lntest.BackendPostgres

	default:
		require.Fail(t, "unknown db backend")
	}

	// Now we can set up our test harness (LND instance), with the chain
	// backend we just created.
	binary := ht.getLndBinary()
	lndHarness, err = lntest.NewNetworkHarness(
		miner, chainBackend, binary, dbBackend,
	)
	if err != nil {
		ht.Fatalf("unable to create lightning network harness: %v", err)
	}
	defer lndHarness.Stop()

	// Spawn a new goroutine to watch for any fatal errors that any of the
	// running lnd processes encounter. If an error occurs, then the test
	// case should naturally as a result and we log the server error here to
	// help debug.
	go func() {
		errChan := lndHarness.ProcessErrors()
		for err := range errChan {
			ht.Logf("lnd finished with error (stderr):\n%v",
				err)

		}
	}()

	// With the dcrd harness created, we can now complete the
	// initialization of the network. args - list of lnd arguments,
	// example: "--debuglevel=debug"
	// TODO(roasbeef): create master balanced channel with all the monies?
	aliceBobArgs := []string{
		"--default-remote-max-htlcs=150",
		"--dust-threshold=5000000",
	}

	err = lndHarness.SetUp(t, "", aliceBobArgs)
	require.NoError(t,
		err, "unable to set up test lightning network",
	)
	defer func() {
		require.NoError(t, lndHarness.TearDown())
	}()

	// Run the subset of the test cases selected in this tranche.
	t.Logf("Running %v integration tests", len(testCases))
	for idx, testCase := range testCases {
		testCase := testCase
		name := fmt.Sprintf("tranche%02d/%02d-of-%d/%s/%s",
			trancheIndex, trancheOffset+uint(idx)+1,
			len(allTestCases), chainBackend.Name(), testCase.name)

		success := t.Run(name, func(t1 *testing.T) {
			cleanTestCaseName := strings.ReplaceAll(
				testCase.name, " ", "_",
			)

			lndHarness.ModifyTestCaseName(cleanTestCaseName)

			logLine := fmt.Sprintf(
				"STARTING ============ %v ============\n",
				testCase.name,
			)

			lndHarness.Alice.AddToLog("%s", logLine)
			lndHarness.Bob.AddToLog("%s", logLine)

			// Start every test with the default static fee estimate.
			lndHarness.SetFeeEstimate(10000)

			// Create a separate harness test for the testcase to
			// avoid overwriting the external harness test that is
			// tied to the parent test.
			ht := newHarnessTest(t1, lndHarness)
			ht.RunTestCase(testCase)
			assertCleanState(ht, lndHarness)
		})

		// Stop at the first failure. Mimic behavior of original test
		// framework.
		if !success {
			// Log failure time to help relate the lnd logs to the
			// failure.
			t.Logf("Failure time: %v", time.Now().Format(
				"2006-01-02 15:04:05.000",
			))
			break
		}
	}
}
