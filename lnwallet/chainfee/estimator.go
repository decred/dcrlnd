package chainfee

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	prand "math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrutil/v4"
	dcrjson "github.com/decred/dcrd/rpc/jsonrpc/types/v3"
	"github.com/decred/dcrd/rpcclient/v7"
)

const (
	// maxBlockTarget is the highest number of blocks confirmations that a
	// WebAPIEstimator will cache fees for. This number is chosen because
	// it's the highest number of confs bitcoind will return a fee estimate
	// for.
	maxBlockTarget uint32 = 1009

	// minBlockTarget is the lowest number of blocks confirmations that
	// a WebAPIEstimator will cache fees for. Requesting an estimate for
	// less than this will result in an error.
	minBlockTarget uint32 = 2

	// minFeeUpdateTimeout represents the minimum interval in which a
	// WebAPIEstimator will request fresh fees from its API.
	minFeeUpdateTimeout = 5 * time.Minute

	// maxFeeUpdateTimeout represents the maximum interval in which a
	// WebAPIEstimator will request fresh fees from its API.
	maxFeeUpdateTimeout = 20 * time.Minute
)

// Estimator provides the ability to estimate on-chain transaction fees for
// various combinations of transaction sizes and desired confirmation time
// (measured by number of blocks).
type Estimator interface {
	// EstimateFeePerKB takes in a target for the number of blocks until
	// an initial confirmation and returns the estimated fee expressed in
	// atoms/byte.
	EstimateFeePerKB(numBlocks uint32) (AtomPerKByte, error)

	// Start signals the Estimator to start any processes or goroutines
	// it needs to perform its duty.
	Start() error

	// Stop stops any spawned goroutines and cleans up the resources used
	// by the fee estimator.
	Stop() error

	// RelayFeePerKB returns the minimum fee rate required for transactions
	// to be relayed. This is also the basis for calculation of the dust
	// limit.
	RelayFeePerKB() AtomPerKByte
}

// StaticEstimator will return a static value for all fee calculation requests.
// It is designed to be replaced by a proper fee calculation implementation.
// The fees are not accessible directly, because changing them would not be
// thread safe.
type StaticEstimator struct {
	// feePerKB is the static fee rate in atoms-per-kB that will be
	// returned by this fee estimator.
	feePerKB AtomPerKByte

	// relayFee is the minimum fee rate required for transactions to be
	// relayed.
	relayFee AtomPerKByte
}

// NewStaticEstimator returns a new static fee estimator instance.
func NewStaticEstimator(feePerKB,
	relayFee AtomPerKByte) *StaticEstimator {

	return &StaticEstimator{
		feePerKB: feePerKB,
		relayFee: relayFee,
	}
}

// EstimateFeePerKB will return the static value for fee calculations.
//
// NOTE: This method is part of the FeeEstimator interface.
func (e StaticEstimator) EstimateFeePerKB(numBlocks uint32) (AtomPerKByte, error) {
	return e.feePerKB, nil
}

// RelayFeePerKB returns the minimum fee rate required for transactions to be
// relayed.
//
// NOTE: This method is part of the FeeEstimator interface.
func (e StaticEstimator) RelayFeePerKB() AtomPerKByte {
	return e.relayFee
}

// Start signals the Estimator to start any processes or goroutines
// it needs to perform its duty.
//
// NOTE: This method is part of the Estimator interface.
func (e StaticEstimator) Start() error {
	return nil
}

// Stop stops any spawned goroutines and cleans up the resources used
// by the fee estimator.
//
// NOTE: This method is part of the Estimator interface.
func (e StaticEstimator) Stop() error {
	return nil
}

// A compile-time assertion to ensure that StaticFeeEstimator implements the
// Estimator interface.
var _ Estimator = (*StaticEstimator)(nil)

// DcrdEstimator is an implementation of the FeeEstimator interface backed by
// the RPC interface of an active dcrd node. This implementation will proxy any
// fee estimation requests to dcrd's RPC interface.
type DcrdEstimator struct {
	// fallbackFeePerKB is the fall back fee rate in atoms/kB that is
	// returned if the fee estimator does not yet have enough data to
	// actually produce fee estimates.
	fallbackFeePerKB AtomPerKByte

	// minFeePerKB is the minimum fee, in atoms/kB, that we should enforce.
	// This will be used as the default fee rate for a transaction when the
	// estimated fee rate is too low to allow the transaction to propagate
	// through the network.
	minFeePerKB AtomPerKByte

	dcrdConn *rpcclient.Client
}

// NewDcrdEstimator creates a new DcrdFeeEstimator given a fully populated rpc
// config that is able to successfully connect and authenticate with the dcrd
// node, and also a fall back fee rate. The fallback fee rate is used in the
// occasion that the estimator has insufficient data, or returns zero for a fee
// estimate.
func NewDcrdEstimator(rpcConfig rpcclient.ConnConfig,
	fallBackFeeRate AtomPerKByte) (*DcrdEstimator, error) {

	rpcConfig.DisableConnectOnNew = true
	rpcConfig.DisableAutoReconnect = false
	chainConn, err := rpcclient.New(&rpcConfig, nil)
	if err != nil {
		return nil, err
	}

	return &DcrdEstimator{
		fallbackFeePerKB: fallBackFeeRate,
		dcrdConn:         chainConn,
	}, nil
}

// Start signals the Estimator to start any processes or goroutines
// it needs to perform its duty.
//
// NOTE: This method is part of the FeeEstimator interface.
func (b *DcrdEstimator) Start() error {
	ctx := context.Background()
	if err := b.dcrdConn.Connect(ctx, true); err != nil {
		return err
	}

	// Once the connection to the backend node has been established, we'll
	// query it for its minimum relay fee.
	info, err := b.dcrdConn.GetInfo(ctx)
	if err != nil {
		return err
	}

	relayFee, err := dcrutil.NewAmount(info.RelayFee)
	if err != nil {
		return err
	}

	// By default, we'll use the backend node's minimum relay fee as the
	// minimum fee rate we'll propose for transactions. However, if this
	// happens to be lower than our fee floor, we'll enforce that instead.
	b.minFeePerKB = AtomPerKByte(relayFee)
	if b.minFeePerKB < FeePerKBFloor {
		log.Warnf("Dcrd returned fee rate of %s which is "+
			"lower than the floor rate", b.minFeePerKB)
		b.minFeePerKB = FeePerKBFloor
	}

	log.Debugf("Using minimum fee rate of %s", b.minFeePerKB)

	return nil
}

// Stop stops any spawned goroutines and cleans up the resources used
// by the fee estimator.
//
// NOTE: This method is part of the FeeEstimator interface.
func (b *DcrdEstimator) Stop() error {
	b.dcrdConn.Shutdown()

	return nil
}

// RelayFeePerKB returns the minimum fee rate required for transactions to be
// relayed.
//
// NOTE: This method is part of the FeeEstimator interface.
func (b *DcrdEstimator) RelayFeePerKB() AtomPerKByte {
	return b.minFeePerKB
}

// EstimateFeePerKB queries the connected chain client for a fee estimation for
// the given block range.
//
// NOTE: This method is part of the FeeEstimator interface.
func (b *DcrdEstimator) EstimateFeePerKB(numBlocks uint32) (AtomPerKByte, error) {
	feeEstimate, err := b.fetchEstimate(numBlocks)
	switch {
	// If the estimator doesn't have enough data, or returns an error, then
	// to return a proper value, then we'll return the default fall back
	// fee rate.
	case err != nil:
		log.Errorf("unable to query estimator: %v", err)
		fallthrough

	case feeEstimate == 0:
		return b.fallbackFeePerKB, nil
	}

	return feeEstimate, nil
}

// fetchEstimate returns a fee estimate for a transaction to be confirmed in
// confTarget blocks. The estimate is returned in atom/kB.
func (b *DcrdEstimator) fetchEstimate(confTarget uint32) (AtomPerKByte, error) {
	// First, we'll fetch the estimate for our confirmation target.
	dcrPerKB, err := b.dcrdConn.EstimateSmartFee(context.TODO(), int64(confTarget),
		dcrjson.EstimateSmartFeeConservative)
	if err != nil {
		return 0, err
	}

	// Next, we'll convert the returned value to atoms, as it's
	// currently returned in DCR.
	atoms, err := dcrutil.NewAmount(dcrPerKB.FeeRate)
	if err != nil {
		return 0, err
	}
	atomsPerKB := AtomPerKByte(atoms)

	// Finally, we'll enforce our fee floor.
	if atomsPerKB < b.minFeePerKB {
		log.Debugf("Estimated fee rate of %s is too low, "+
			"using fee floor of %s", atomsPerKB,
			b.minFeePerKB)
		atomsPerKB = b.minFeePerKB
	}

	log.Debugf("Returning %s for conf target of %d",
		atomsPerKB, confTarget)

	return atomsPerKB, nil
}

// A compile-time assertion to ensure that DcrdFeeEstimator implements the
// FeeEstimator interface.
var _ Estimator = (*DcrdEstimator)(nil)

// WebAPIFeeSource is an interface allows the WebAPIEstimator to query an
// arbitrary HTTP-based fee estimator. Each new set/network will gain an
// implementation of this interface in order to allow the WebAPIEstimator to
// be fully generic in its logic.
type WebAPIFeeSource interface {
	// GenQueryURL generates the full query URL. The value returned by this
	// method should be able to be used directly as a path for an HTTP GET
	// request.
	GenQueryURL() string

	// ParseResponse attempts to parse the body of the response generated
	// by the above query URL. Typically this will be JSON, but the
	// specifics are left to the WebAPIFeeSource implementation.
	ParseResponse(r io.Reader) (map[uint32]uint32, error)
}

// SparseConfFeeSource is an implementation of the WebAPIFeeSource that utilizes
// a user-specified fee estimation API for Bitcoin. It expects the response
// to be in the JSON format: `fee_by_block_target: { ... }` where the value maps
// block targets to fee estimates (in sat per kilovbyte).
type SparseConfFeeSource struct {
	// URL is the fee estimation API specified by the user.
	URL string
}

// GenQueryURL generates the full query URL. The value returned by this
// method should be able to be used directly as a path for an HTTP GET
// request.
//
// NOTE: Part of the WebAPIFeeSource interface.
func (s SparseConfFeeSource) GenQueryURL() string {
	return s.URL
}

// ParseResponse attempts to parse the body of the response generated by the
// above query URL. Typically this will be JSON, but the specifics are left to
// the WebAPIFeeSource implementation.
//
// NOTE: Part of the WebAPIFeeSource interface.
func (s SparseConfFeeSource) ParseResponse(r io.Reader) (map[uint32]uint32, error) {
	type jsonResp struct {
		FeeByBlockTarget map[uint32]uint32 `json:"fee_by_block_target"`
	}

	resp := jsonResp{
		FeeByBlockTarget: make(map[uint32]uint32),
	}
	jsonReader := json.NewDecoder(r)
	if err := jsonReader.Decode(&resp); err != nil {
		return nil, err
	}

	return resp.FeeByBlockTarget, nil
}

// A compile-time assertion to ensure that SparseConfFeeSource implements the
// WebAPIFeeSource interface.
var _ WebAPIFeeSource = (*SparseConfFeeSource)(nil)

// WebAPIEstimator is an implementation of the Estimator interface that
// queries an HTTP-based fee estimation from an existing web API.
type WebAPIEstimator struct {
	started sync.Once
	stopped sync.Once

	// apiSource is the backing web API source we'll use for our queries.
	apiSource WebAPIFeeSource

	// updateFeeTicker is the ticker responsible for updating the Estimator's
	// fee estimates every time it fires.
	updateFeeTicker *time.Ticker

	// feeByBlockTarget is our cache for fees pulled from the API. When a
	// fee estimate request comes in, we pull the estimate from this array
	// rather than re-querying the API, to prevent an inadvertent DoS attack.
	feesMtx          sync.Mutex
	feeByBlockTarget map[uint32]uint32

	// defaultFeePerKB is a fallback value that we'll use if we're unable
	// to query the API for any reason.
	defaultFeePerKB AtomPerKByte

	// netGetter performs a GET http request to the specified URL and
	// returns the response. It is exposed here to allow tests to mock the
	// network.
	netGetter func(url string) (*http.Response, error)

	quit chan struct{}
	wg   sync.WaitGroup
}

// defaultNetGetter performs a GET request to the specified URL or times out in
// at most 10 seconds.
func defaultNetGetter(url string) (*http.Response, error) {
	// Rather than use the default http.Client, we'll make a custom one
	// which will allow us to control how long we'll wait to read the
	// response from the service. This way, if the service is down or
	// overloaded, we can exit early and use our default fee.
	netTransport := &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	netClient := &http.Client{
		Timeout:   time.Second * 10,
		Transport: netTransport,
	}

	return netClient.Get(url)
}

// NewWebAPIEstimator creates a new WebAPIEstimator from a given URL and a
// fallback default fee. The fees are updated whenever a new block is mined.
func NewWebAPIEstimator(
	api WebAPIFeeSource, defaultFee AtomPerKByte) *WebAPIEstimator {

	return &WebAPIEstimator{
		apiSource:        api,
		feeByBlockTarget: make(map[uint32]uint32),
		defaultFeePerKB:  defaultFee,
		netGetter:        defaultNetGetter,
		quit:             make(chan struct{}),
	}
}

// EstimateFeePerKB takes in a target for the number of blocks until an initial
// confirmation and returns the estimated fee expressed in sat/kw.
//
// NOTE: This method is part of the FeeEstimator interface.
func (w *WebAPIEstimator) EstimateFeePerKB(numBlocks uint32) (AtomPerKByte, error) {
	if numBlocks > maxBlockTarget {
		numBlocks = maxBlockTarget
	} else if numBlocks < minBlockTarget {
		return 0, fmt.Errorf("conf target of %v is too low, minimum "+
			"accepted is %v", numBlocks, minBlockTarget)
	}

	feePerKB, err := w.getCachedFee(numBlocks)
	if err != nil {
		return 0, err
	}
	atomsPerKB := AtomPerKByte(feePerKB)

	// If the result is too low, then we'll clamp it to our current fee
	// floor.
	if atomsPerKB < FeePerKBFloor {
		atomsPerKB = FeePerKBFloor
	}

	log.Debugf("Web API returning %v atoms/KB for conf target of %v",
		atomsPerKB, numBlocks)

	return atomsPerKB, nil
}

// Start signals the Estimator to start any processes or goroutines it needs
// to perform its duty.
//
// NOTE: This method is part of the Estimator interface.
func (w *WebAPIEstimator) Start() error {
	var err error
	w.started.Do(func() {
		log.Infof("Starting web API fee estimator")

		w.updateFeeTicker = time.NewTicker(w.randomFeeUpdateTimeout())
		w.updateFeeEstimates()

		w.wg.Add(1)
		go w.feeUpdateManager()

	})
	return err
}

// Stop stops any spawned goroutines and cleans up the resources used by the
// fee estimator.
//
// NOTE: This method is part of the Estimator interface.
func (w *WebAPIEstimator) Stop() error {
	w.stopped.Do(func() {
		log.Infof("Stopping web API fee estimator")

		w.updateFeeTicker.Stop()

		close(w.quit)
		w.wg.Wait()
	})
	return nil
}

// RelayFeePerKB returns the minimum fee rate required for transactions to be
// relayed.
//
// NOTE: This method is part of the FeeEstimator interface.
func (w *WebAPIEstimator) RelayFeePerKB() AtomPerKByte {
	return FeePerKBFloor
}

// randomFeeUpdateTimeout returns a random timeout between minFeeUpdateTimeout
// and maxFeeUpdateTimeout that will be used to determine how often the Estimator
// should retrieve fresh fees from its API.
func (w *WebAPIEstimator) randomFeeUpdateTimeout() time.Duration {
	lower := int64(minFeeUpdateTimeout)
	upper := int64(maxFeeUpdateTimeout)
	return time.Duration(prand.Int63n(upper-lower) + lower)
}

// getCachedFee takes in a target for the number of blocks until an initial
// confirmation and returns an estimated fee (if one was returned by the API). If
// the fee was not previously cached, we cache it here.
func (w *WebAPIEstimator) getCachedFee(numBlocks uint32) (uint32, error) {
	w.feesMtx.Lock()
	defer w.feesMtx.Unlock()

	// Search our cached fees for the desired block target. If the target is
	// not cached, then attempt to extrapolate it from the next lowest target
	// that *is* cached. If we successfully extrapolate, then cache the
	// target's fee.
	for target := numBlocks; target >= minBlockTarget; target-- {
		fee, ok := w.feeByBlockTarget[target]
		if !ok {
			continue
		}

		_, ok = w.feeByBlockTarget[numBlocks]
		if !ok {
			w.feeByBlockTarget[numBlocks] = fee
		}
		return fee, nil
	}
	return 0, fmt.Errorf("web API does not include a fee estimation for "+
		"block target of %v", numBlocks)
}

// updateFeeEstimates re-queries the API for fresh fees and caches them.
func (w *WebAPIEstimator) updateFeeEstimates() {
	// With the client created, we'll query the API source to fetch the URL
	// that we should use to query for the fee estimation.
	targetURL := w.apiSource.GenQueryURL()
	resp, err := w.netGetter(targetURL)
	if err != nil {
		log.Errorf("unable to query web api for fee response: %v",
			err)
		return
	}
	defer resp.Body.Close()

	// Once we've obtained the response, we'll instruct the WebAPIFeeSource
	// to parse out the body to obtain our final result.
	feesByBlockTarget, err := w.apiSource.ParseResponse(resp.Body)
	if err != nil {
		log.Errorf("unable to query web api for fee response: %v",
			err)
		return
	}

	w.feesMtx.Lock()
	w.feeByBlockTarget = feesByBlockTarget
	w.feesMtx.Unlock()
}

// feeUpdateManager updates the fee estimates whenever a new block comes in.
func (w *WebAPIEstimator) feeUpdateManager() {
	defer w.wg.Done()

	for {
		select {
		case <-w.updateFeeTicker.C:
			w.updateFeeEstimates()
		case <-w.quit:
			return
		}
	}
}

// A compile-time assertion to ensure that WebAPIEstimator implements the
// Estimator interface.
var _ Estimator = (*WebAPIEstimator)(nil)

// dcrdataFeeExpiration is how long a cached fee is good for. After this time,
// a new HTTP request is performed on the next EstimateFeePerKB.
const (
	// How long before fetched fee rates expire.
	dcrdataFeeExpiration  = time.Minute * 5
	dcrdataRequestTimeout = time.Second * 10
	// How long to wait after a failed request before trying again, returning
	// the fallback rate in the interim.
	dcrdataFailExpiration = time.Minute
)

// dcrdataFeeEstimate is the result of a fee request. dcrdataFeeEstimate will be
// cached for up to dcrdataFeeExpiration.
type dcrdataFeeEstimate struct {
	feeRate AtomPerKByte
	stamp   time.Time
}

// DCRDataFeeEstimator is a fee estimator based on the common public dcrdata
// servers. DCRDataFeeEstimator's EstimateFeePerKB method will never return an
// error. In the case of a network error, an error is logged and the
// fallbackRate is returned.
type DCRDataFeeEstimator struct {
	urlTemplate  string
	fallbackRate AtomPerKByte
	fetchRate    func(context.Context, string, uint32) (AtomPerKByte, error)

	ctx    context.Context
	cancel context.CancelFunc

	cacheMtx sync.Mutex
	cache    map[uint32]*dcrdataFeeEstimate
	lastFail time.Time
}

// DCRDataEstimator is a constructor for a new DCRDataFeeEstimator. The
// fallbackRate will be returned without accompanying error in the case of any
// network errors.
func NewDCRDataEstimator(testnet bool, fallbackRate AtomPerKByte) *DCRDataFeeEstimator {
	url := "https://explorer.dcrdata.org/insight/api/utils/estimatefee?nbBlocks=%d"
	if testnet {
		url = "https://testnet.dcrdata.org/insight/api/utils/estimatefee?nbBlocks=%d"
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &DCRDataFeeEstimator{
		urlTemplate:  url,
		fallbackRate: fallbackRate,
		fetchRate:    requestDCRDataFeeRate,
		ctx:          ctx,
		cancel:       cancel,
		cache:        make(map[uint32]*dcrdataFeeEstimate, 4),
	}
}

// EstimateFeePerKB takes in a target for the number of blocks until an initial
// confirmation and returns the estimated fee expressed in atoms/byte.
// IMPORTANT: Fee estimates are lazy-loaded and cached for up to
// dcrdataFeeExpiration. A request can block for up to dcrdataRequestTimeout,
// and requests are run under mutex lock to prevent duplicate requests from
// running simultaneously.
func (d *DCRDataFeeEstimator) EstimateFeePerKB(numBlocks uint32) (AtomPerKByte, error) {
	d.cacheMtx.Lock()
	defer d.cacheMtx.Unlock()

	cachedEstimate, found := d.cache[numBlocks]
	if found && time.Now().Before(cachedEstimate.stamp.Add(dcrdataFeeExpiration)) {
		return cachedEstimate.feeRate, nil
	}

	if time.Since(d.lastFail) < dcrdataFailExpiration {
		log.Debugf("DCRDataFeeEstimator returning fallback rate for cached request failure")
		return d.fallbackRate, nil
	}

	url := fmt.Sprintf(d.urlTemplate, numBlocks)
	feeRate, err := d.fetchRate(d.ctx, url, numBlocks)
	if err != nil {
		// We'll log it as an Error, but return the fallback rate without an
		// error.
		log.Errorf("error getting dcrdata fee rate: %v", err)
		log.Error("using fallback rate of %d", d.fallbackRate)
		d.lastFail = time.Now()
		return d.fallbackRate, nil
	}

	d.cache[numBlocks] = &dcrdataFeeEstimate{
		feeRate: feeRate,
		stamp:   time.Now(),
	}

	return feeRate, nil
}

// Start signals the Estimator to start any processes or goroutines it needs
// to perform its duty.
//
// NOTE: This method is part of the Estimator interface. This implementation
// does nothing.
func (d *DCRDataFeeEstimator) Start() error {
	return nil
}

// Stop stops any spawned goroutines and cleans up the resources used by the
// fee estimator.
//
// NOTE: This method is part of the Estimator interface.
func (d *DCRDataFeeEstimator) Stop() error {
	d.cancel()
	return nil
}

func (d *DCRDataFeeEstimator) RelayFeePerKB() AtomPerKByte {
	return FeePerKBFloor
}

// requestDCRDataFeeRate is the default requester for dcrdata fee rates.
func requestDCRDataFeeRate(ctx context.Context, url string, numBlocks uint32) (AtomPerKByte, error) {
	ctx, cancel := context.WithTimeout(ctx, dcrdataRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("error constructing request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("error fetching fees: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode > 299 {
		return 0, fmt.Errorf("status code %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("error reading response: %v", err)
	}
	var feeMap map[uint32]float64
	if err := json.Unmarshal(body, &feeMap); err != nil {
		return 0, fmt.Errorf("error decoding fee response: %v", err)
	}

	feeDcrPerKB, found := feeMap[numBlocks]
	if !found {
		return 0, fmt.Errorf("response did not include requested fee")
	}

	feeAtomsPerKB, err := dcrutil.NewAmount(feeDcrPerKB)
	if err != nil {
		return 0, fmt.Errorf("error converting fee rate: %v", err)
	}

	return AtomPerKByte(feeAtomsPerKB), nil
}

var _ Estimator = (*DCRDataFeeEstimator)(nil)
