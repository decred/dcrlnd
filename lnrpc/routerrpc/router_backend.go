package routerrpc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	math "math"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v2"

	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntypes"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/record"
	"github.com/decred/dcrlnd/routing"
	"github.com/decred/dcrlnd/routing/route"
	"github.com/decred/dcrlnd/tlv"
	"github.com/decred/dcrlnd/zpay32"
)

// RouterBackend contains the backend implementation of the router rpc sub
// server calls.
type RouterBackend struct {
	// MaxPaymentMAtoms is the largest payment permitted by the backend.
	MaxPaymentMAtoms lnwire.MilliAtom

	// SelfNode is the vertex of the node sending the payment.
	SelfNode route.Vertex

	// FetchChannelCapacity is a closure that we'll use the fetch the total
	// capacity of a channel to populate in responses.
	FetchChannelCapacity func(chanID uint64) (dcrutil.Amount, error)

	// FetchChannelEndpoints returns the pubkeys of both endpoints of the
	// given channel id.
	FetchChannelEndpoints func(chanID uint64) (route.Vertex,
		route.Vertex, error)

	// FindRoute is a closure that abstracts away how we locate/query for
	// routes.
	FindRoute func(source, target route.Vertex,
		amt lnwire.MilliAtom, restrictions *routing.RestrictParams,
		destTlvRecords []tlv.Record,
		finalExpiry ...uint16) (*route.Route, error)

	MissionControl MissionControl

	// ActiveNetParams are the network parameters of the primary network
	// that the route is operating on. This is necessary so we can ensure
	// that we receive payment requests that send to destinations on our
	// network.
	ActiveNetParams *chaincfg.Params

	// Tower is the ControlTower instance that is used to track pending
	// payments.
	Tower routing.ControlTower

	// MaxTotalTimelock is the maximum total time lock a route is allowed to
	// have.
	MaxTotalTimelock uint32
}

// MissionControl defines the mission control dependencies of routerrpc.
type MissionControl interface {
	// GetProbability is expected to return the success probability of a
	// payment from fromNode to toNode.
	GetProbability(fromNode, toNode route.Vertex,
		amt lnwire.MilliAtom) float64

	// ResetHistory resets the history of MissionControl returning it to a
	// state as if no payment attempts have been made.
	ResetHistory() error

	// GetHistorySnapshot takes a snapshot from the current mission control
	// state and actual probability estimates.
	GetHistorySnapshot() *routing.MissionControlSnapshot

	// GetPairHistorySnapshot returns the stored history for a given node
	// pair.
	GetPairHistorySnapshot(fromNode,
		toNode route.Vertex) routing.TimedPairResult
}

// QueryRoutes attempts to query the daemons' Channel Router for a possible
// route to a target destination capable of carrying a specific amount of
// satoshis within the route's flow. The retuned route contains the full
// details required to craft and send an HTLC, also including the necessary
// information that should be present within the Sphinx packet encapsulated
// within the HTLC.
//
// TODO(roasbeef): should return a slice of routes in reality * create separate
// PR to send based on well formatted route
func (r *RouterBackend) QueryRoutes(ctx context.Context,
	in *lnrpc.QueryRoutesRequest) (*lnrpc.QueryRoutesResponse, error) {

	parsePubKey := func(key string) (route.Vertex, error) {
		pubKeyBytes, err := hex.DecodeString(key)
		if err != nil {
			return route.Vertex{}, err
		}

		return route.NewVertexFromBytes(pubKeyBytes)
	}

	// Parse the hex-encoded source and target public keys into full public
	// key objects we can properly manipulate.
	targetPubKey, err := parsePubKey(in.PubKey)
	if err != nil {
		return nil, err
	}

	var sourcePubKey route.Vertex
	if in.SourcePubKey != "" {
		var err error
		sourcePubKey, err = parsePubKey(in.SourcePubKey)
		if err != nil {
			return nil, err
		}
	} else {
		// If no source is specified, use self.
		sourcePubKey = r.SelfNode
	}

	// Currently, within the bootstrap phase of the network, we limit the
	// largest payment size allotted to (2^32) - 1 mSAT or 4.29 million
	// satoshis.
	amt, err := lnrpc.UnmarshallAmt(in.Amt, in.AmtMAtoms)
	if err != nil {
		return nil, err
	}
	if amt > r.MaxPaymentMAtoms {
		return nil, fmt.Errorf("payment of %v is too large, max payment "+
			"allowed is %v", amt, r.MaxPaymentMAtoms.ToAtoms())
	}

	// Unmarshall restrictions from request.
	feeLimit := lnrpc.CalculateFeeLimit(in.FeeLimit, amt)

	ignoredNodes := make(map[route.Vertex]struct{})
	for _, ignorePubKey := range in.IgnoredNodes {
		ignoreVertex, err := route.NewVertexFromBytes(ignorePubKey)
		if err != nil {
			return nil, err
		}
		ignoredNodes[ignoreVertex] = struct{}{}
	}

	ignoredPairs := make(map[routing.DirectedNodePair]struct{})

	// Convert deprecated ignoredEdges to pairs.
	for _, ignoredEdge := range in.IgnoredEdges {
		pair, err := r.rpcEdgeToPair(ignoredEdge)
		if err != nil {
			log.Warnf("Ignore channel %v skipped: %v",
				ignoredEdge.ChannelId, err)

			continue
		}
		ignoredPairs[pair] = struct{}{}
	}

	// Add ignored pairs to set.
	for _, ignorePair := range in.IgnoredPairs {
		from, err := route.NewVertexFromBytes(ignorePair.From)
		if err != nil {
			return nil, err
		}

		to, err := route.NewVertexFromBytes(ignorePair.To)
		if err != nil {
			return nil, err
		}

		pair := routing.NewDirectedNodePair(from, to)
		ignoredPairs[pair] = struct{}{}
	}

	// Since QueryRoutes allows having a different source other than
	// ourselves, we'll only apply our max time lock if we are the source.
	maxTotalTimelock := r.MaxTotalTimelock
	if sourcePubKey != r.SelfNode {
		maxTotalTimelock = math.MaxUint32
	}
	cltvLimit, err := ValidateCLTVLimit(in.CltvLimit, maxTotalTimelock)
	if err != nil {
		return nil, err
	}

	// We need to subtract the final delta before passing it into path
	// finding. The optimal path is independent of the final cltv delta and
	// the path finding algorithm is unaware of this value.
	finalCLTVDelta := uint16(zpay32.DefaultFinalCLTVDelta)
	if in.FinalCltvDelta != 0 {
		finalCLTVDelta = uint16(in.FinalCltvDelta)
	}
	cltvLimit -= uint32(finalCLTVDelta)

	var destTLV map[uint64][]byte
	restrictions := &routing.RestrictParams{
		FeeLimit: feeLimit,
		ProbabilitySource: func(fromNode, toNode route.Vertex,
			amt lnwire.MilliAtom) float64 {

			if _, ok := ignoredNodes[fromNode]; ok {
				return 0
			}

			pair := routing.DirectedNodePair{
				From: fromNode,
				To:   toNode,
			}
			if _, ok := ignoredPairs[pair]; ok {
				return 0
			}

			if !in.UseMissionControl {
				return 1
			}

			return r.MissionControl.GetProbability(
				fromNode, toNode, amt,
			)
		},
		DestPayloadTLV: len(destTLV) != 0,
		CltvLimit:      cltvLimit,
	}

	// If we have any TLV records destined for the final hop, then we'll
	// attempt to decode them now into a form that the router can more
	// easily manipulate.
	destTlvRecords := tlv.MapToRecords(destTLV)

	// Query the channel router for a possible path to the destination that
	// can carry `in.Amt` satoshis _including_ the total fee required on
	// the route.
	route, err := r.FindRoute(
		sourcePubKey, targetPubKey, amt, restrictions,
		destTlvRecords, finalCLTVDelta,
	)
	if err != nil {
		return nil, err
	}

	// For each valid route, we'll convert the result into the format
	// required by the RPC system.
	rpcRoute, err := r.MarshallRoute(route)
	if err != nil {
		return nil, err
	}

	// Calculate route success probability. Do not rely on a probability
	// that could have been returned from path finding, because mission
	// control may have been disabled in the provided ProbabilitySource.
	successProb := r.getSuccessProbability(route)

	routeResp := &lnrpc.QueryRoutesResponse{
		Routes:      []*lnrpc.Route{rpcRoute},
		SuccessProb: successProb,
	}

	return routeResp, nil
}

// getSuccessProbability returns the success probability for the given route
// based on the current state of mission control.
func (r *RouterBackend) getSuccessProbability(rt *route.Route) float64 {
	fromNode := rt.SourcePubKey
	amtToFwd := rt.TotalAmount
	successProb := 1.0
	for _, hop := range rt.Hops {
		toNode := hop.PubKeyBytes

		probability := r.MissionControl.GetProbability(
			fromNode, toNode, amtToFwd,
		)

		successProb *= probability

		amtToFwd = hop.AmtToForward
		fromNode = toNode
	}

	return successProb
}

// rpcEdgeToPair looks up the provided channel and returns the channel endpoints
// as a directed pair.
func (r *RouterBackend) rpcEdgeToPair(e *lnrpc.EdgeLocator) (
	routing.DirectedNodePair, error) {

	a, b, err := r.FetchChannelEndpoints(e.ChannelId)
	if err != nil {
		return routing.DirectedNodePair{}, err
	}

	var pair routing.DirectedNodePair
	if e.DirectionReverse {
		pair.From, pair.To = b, a
	} else {
		pair.From, pair.To = a, b
	}

	return pair, nil
}

// MarshallRoute marshalls an internal route to an rpc route struct.
func (r *RouterBackend) MarshallRoute(route *route.Route) (*lnrpc.Route, error) {
	resp := &lnrpc.Route{
		TotalTimeLock:   route.TotalTimeLock,
		TotalFees:       int64(route.TotalFees().ToAtoms()),
		TotalFeesMAtoms: int64(route.TotalFees()),
		TotalAmt:        int64(route.TotalAmount.ToAtoms()),
		TotalAmtMAtoms:  int64(route.TotalAmount),
		Hops:            make([]*lnrpc.Hop, len(route.Hops)),
	}
	incomingAmt := route.TotalAmount
	for i, hop := range route.Hops {
		fee := route.HopFee(i)

		// Channel capacity is not a defining property of a route. For
		// backwards RPC compatibility, we retrieve it here from the
		// graph.
		chanCapacity, err := r.FetchChannelCapacity(hop.ChannelID)
		if err != nil {
			// If capacity cannot be retrieved, this may be a
			// not-yet-received or private channel. Then report
			// amount that is sent through the channel as capacity.
			chanCapacity = incomingAmt.ToAtoms()
		}

		// Extract the MPP fields if present on this hop.
		var mpp *lnrpc.MPPRecord
		if hop.MPP != nil {
			addr := hop.MPP.PaymentAddr()

			mpp = &lnrpc.MPPRecord{
				PaymentAddr:    addr[:],
				TotalAmtMAtoms: int64(hop.MPP.TotalMAtoms()),
			}
		}

		resp.Hops[i] = &lnrpc.Hop{
			ChanId:             hop.ChannelID,
			ChanCapacity:       int64(chanCapacity),
			AmtToForward:       int64(hop.AmtToForward.ToAtoms()),
			AmtToForwardMAtoms: int64(hop.AmtToForward),
			Fee:                int64(fee.ToAtoms()),
			FeeMAtoms:          int64(fee),
			Expiry:             hop.OutgoingTimeLock,
			PubKey: hex.EncodeToString(
				hop.PubKeyBytes[:],
			),
			TlvPayload: !hop.LegacyPayload,
			MppRecord:  mpp,
		}
		incomingAmt = hop.AmtToForward
	}

	return resp, nil
}

// UnmarshallHopWithPubkey unmarshalls an rpc hop for which the pubkey has
// already been extracted.
func UnmarshallHopWithPubkey(rpcHop *lnrpc.Hop, pubkey route.Vertex) (*route.Hop,
	error) {

	var tlvRecords []tlv.Record

	mpp, err := UnmarshalMPP(rpcHop.MppRecord)
	if err != nil {
		return nil, err
	}

	return &route.Hop{
		OutgoingTimeLock: rpcHop.Expiry,
		AmtToForward:     lnwire.MilliAtom(rpcHop.AmtToForwardMAtoms),
		PubKeyBytes:      pubkey,
		ChannelID:        rpcHop.ChanId,
		TLVRecords:       tlvRecords,
		LegacyPayload:    !rpcHop.TlvPayload,
		MPP:              mpp,
	}, nil
}

// UnmarshallHop unmarshalls an rpc hop that may or may not contain a node
// pubkey.
func (r *RouterBackend) UnmarshallHop(rpcHop *lnrpc.Hop,
	prevNodePubKey [33]byte) (*route.Hop, error) {

	var pubKeyBytes [33]byte
	if rpcHop.PubKey != "" {
		// Unmarshall the provided hop pubkey.
		pubKey, err := hex.DecodeString(rpcHop.PubKey)
		if err != nil {
			return nil, fmt.Errorf("cannot decode pubkey %s",
				rpcHop.PubKey)
		}
		copy(pubKeyBytes[:], pubKey)
	} else {
		// If no pub key is given of the hop, the local channel graph
		// needs to be queried to complete the information necessary for
		// routing. Discard edge policies, because they may be nil.
		node1, node2, err := r.FetchChannelEndpoints(rpcHop.ChanId)
		if err != nil {
			return nil, err
		}

		switch {
		case prevNodePubKey == node1:
			pubKeyBytes = node2
		case prevNodePubKey == node2:
			pubKeyBytes = node1
		default:
			return nil, fmt.Errorf("channel edge does not match " +
				"expected node")
		}
	}

	return UnmarshallHopWithPubkey(rpcHop, pubKeyBytes)
}

// UnmarshallRoute unmarshalls an rpc route. For hops that don't specify a
// pubkey, the channel graph is queried.
func (r *RouterBackend) UnmarshallRoute(rpcroute *lnrpc.Route) (
	*route.Route, error) {

	prevNodePubKey := r.SelfNode

	hops := make([]*route.Hop, len(rpcroute.Hops))
	for i, hop := range rpcroute.Hops {
		routeHop, err := r.UnmarshallHop(hop, prevNodePubKey)
		if err != nil {
			return nil, err
		}

		hops[i] = routeHop

		prevNodePubKey = routeHop.PubKeyBytes
	}

	route, err := route.NewRouteFromHops(
		lnwire.MilliAtom(rpcroute.TotalAmtMAtoms),
		rpcroute.TotalTimeLock,
		r.SelfNode,
		hops,
	)
	if err != nil {
		return nil, err
	}

	return route, nil
}

// extractIntentFromSendRequest attempts to parse the SendRequest details
// required to dispatch a client from the information presented by an RPC
// client.
func (r *RouterBackend) extractIntentFromSendRequest(
	rpcPayReq *SendPaymentRequest) (*routing.LightningPayment, error) {

	payIntent := &routing.LightningPayment{}

	// Pass along an outgoing channel restriction if specified.
	if rpcPayReq.OutgoingChanId != 0 {
		payIntent.OutgoingChannelID = &rpcPayReq.OutgoingChanId
	}

	// Pass along a last hop restriction if specified.
	if len(rpcPayReq.LastHopPubkey) > 0 {
		lastHop, err := route.NewVertexFromBytes(
			rpcPayReq.LastHopPubkey,
		)
		if err != nil {
			return nil, err
		}
		payIntent.LastHop = &lastHop
	}

	// Take the CLTV limit from the request if set, otherwise use the max.
	cltvLimit, err := ValidateCLTVLimit(
		uint32(rpcPayReq.CltvLimit), r.MaxTotalTimelock,
	)
	if err != nil {
		return nil, err
	}
	payIntent.CltvLimit = cltvLimit

	// Take fee limit from request.
	payIntent.FeeLimit, err = lnrpc.UnmarshallAmt(
		rpcPayReq.FeeLimitAtoms, rpcPayReq.FeeLimitMAtoms,
	)
	if err != nil {
		return nil, err
	}

	// Set payment attempt timeout.
	if rpcPayReq.TimeoutSeconds == 0 {
		return nil, errors.New("timeout_seconds must be specified")
	}

	var destTLV map[uint64][]byte
	if len(destTLV) != 0 {
		payIntent.FinalDestRecords = tlv.MapToRecords(destTLV)
	}

	payIntent.PayAttemptTimeout = time.Second *
		time.Duration(rpcPayReq.TimeoutSeconds)

	// Route hints.
	routeHints, err := unmarshallRouteHints(
		rpcPayReq.RouteHints,
	)
	if err != nil {
		return nil, err
	}
	payIntent.RouteHints = routeHints

	// Unmarshall either sat or msat amount from request.
	reqAmt, err := lnrpc.UnmarshallAmt(
		rpcPayReq.Amt, rpcPayReq.AmtMAtoms,
	)
	if err != nil {
		return nil, err
	}

	// If the payment request field isn't blank, then the details of the
	// invoice are encoded entirely within the encoded payReq.  So we'll
	// attempt to decode it, populating the payment accordingly.
	if rpcPayReq.PaymentRequest != "" {
		switch {

		case len(rpcPayReq.Dest) > 0:
			return nil, errors.New("dest and payment_request " +
				"cannot appear together")

		case len(rpcPayReq.PaymentHash) > 0:
			return nil, errors.New("dest and payment_hash " +
				"cannot appear together")

		case rpcPayReq.FinalCltvDelta != 0:
			return nil, errors.New("dest and final_cltv_delta " +
				"cannot appear together")
		}

		payReq, err := zpay32.Decode(
			rpcPayReq.PaymentRequest, r.ActiveNetParams,
		)
		if err != nil {
			return nil, err
		}

		// Next, we'll ensure that this payreq hasn't already expired.
		err = ValidatePayReqExpiry(payReq)
		if err != nil {
			return nil, err
		}

		// If the amount was not included in the invoice, then we let
		// the payee specify the amount of satoshis they wish to send.
		// We override the amount to pay with the amount provided from
		// the payment request.
		if payReq.MilliAt == nil {
			if reqAmt == 0 {
				return nil, errors.New("amount must be " +
					"specified when paying a zero amount " +
					"invoice")
			}

			payIntent.Amount = reqAmt
		} else {
			if reqAmt != 0 {
				return nil, errors.New("amount must not be " +
					"specified when paying a non-zero " +
					" amount invoice")
			}

			payIntent.Amount = *payReq.MilliAt
		}

		copy(payIntent.PaymentHash[:], payReq.PaymentHash[:])
		destKey := payReq.Destination.SerializeCompressed()
		copy(payIntent.Target[:], destKey)

		payIntent.FinalCLTVDelta = uint16(payReq.MinFinalCLTVExpiry())
		payIntent.RouteHints = append(
			payIntent.RouteHints, payReq.RouteHints...,
		)
	} else {
		// Otherwise, If the payment request field was not specified
		// (and a custom route wasn't specified), construct the payment
		// from the other fields.

		// Payment destination.
		target, err := route.NewVertexFromBytes(rpcPayReq.Dest)
		if err != nil {
			return nil, err
		}
		payIntent.Target = target

		// Final payment CLTV delta.
		if rpcPayReq.FinalCltvDelta != 0 {
			payIntent.FinalCLTVDelta =
				uint16(rpcPayReq.FinalCltvDelta)
		} else {
			payIntent.FinalCLTVDelta = zpay32.DefaultFinalCLTVDelta
		}

		// Amount.
		if reqAmt == 0 {
			return nil, errors.New("amount must be specified")
		}

		payIntent.Amount = reqAmt

		// Payment hash.
		copy(payIntent.PaymentHash[:], rpcPayReq.PaymentHash)
	}

	// Currently, within the bootstrap phase of the network, we limit the
	// largest payment size allotted to (2^32) - 1 mSAT or 4.29 million
	// satoshis.
	if payIntent.Amount > r.MaxPaymentMAtoms {
		// In this case, we'll send an error to the caller, but
		// continue our loop for the next payment.
		return payIntent, fmt.Errorf("payment of %v is too large, "+
			"max payment allowed is %v", payIntent.Amount,
			r.MaxPaymentMAtoms)

	}

	// Check for disallowed payments to self.
	if !rpcPayReq.AllowSelfPayment && payIntent.Target == r.SelfNode {
		return nil, errors.New("self-payments not allowed")
	}

	return payIntent, nil
}

// unmarshallRouteHints unmarshalls a list of route hints.
func unmarshallRouteHints(rpcRouteHints []*lnrpc.RouteHint) (
	[][]zpay32.HopHint, error) {

	routeHints := make([][]zpay32.HopHint, 0, len(rpcRouteHints))
	for _, rpcRouteHint := range rpcRouteHints {
		routeHint := make(
			[]zpay32.HopHint, 0, len(rpcRouteHint.HopHints),
		)
		for _, rpcHint := range rpcRouteHint.HopHints {
			hint, err := unmarshallHopHint(rpcHint)
			if err != nil {
				return nil, err
			}

			routeHint = append(routeHint, hint)
		}
		routeHints = append(routeHints, routeHint)
	}

	return routeHints, nil
}

// unmarshallHopHint unmarshalls a single hop hint.
func unmarshallHopHint(rpcHint *lnrpc.HopHint) (zpay32.HopHint, error) {
	pubBytes, err := hex.DecodeString(rpcHint.NodeId)
	if err != nil {
		return zpay32.HopHint{}, err
	}

	pubkey, err := secp256k1.ParsePubKey(pubBytes)
	if err != nil {
		return zpay32.HopHint{}, err
	}

	return zpay32.HopHint{
		NodeID:                    pubkey,
		ChannelID:                 rpcHint.ChanId,
		FeeBaseMAtoms:             rpcHint.FeeBaseMAtoms,
		FeeProportionalMillionths: rpcHint.FeeProportionalMillionths,
		CLTVExpiryDelta:           uint16(rpcHint.CltvExpiryDelta),
	}, nil
}

// ValidatePayReqExpiry checks if the passed payment request has expired. In
// the case it has expired, an error will be returned.
func ValidatePayReqExpiry(payReq *zpay32.Invoice) error {
	expiry := payReq.Expiry()
	validUntil := payReq.Timestamp.Add(expiry)
	if time.Now().After(validUntil) {
		return fmt.Errorf("invoice expired. Valid until %v", validUntil)
	}

	return nil
}

// ValidateCLTVLimit returns a valid CLTV limit given a value and a maximum. If
// the value exceeds the maximum, then an error is returned. If the value is 0,
// then the maximum is used.
func ValidateCLTVLimit(val, max uint32) (uint32, error) {
	switch {
	case val == 0:
		return max, nil
	case val > max:
		return 0, fmt.Errorf("total time lock of %v exceeds max "+
			"allowed %v", val, max)
	default:
		return val, nil
	}
}

// UnmarshalMPP accepts the mpp_total_amt_m_atoms and mpp_payment_addr fields
// from an RPC request and converts into an record.MPP object. An error is
// returned if the payment address is not 0 or 32 bytes. If the total amount
// and payment address are zero-value, the return value will be nil signaling
// there is no MPP record to attach to this hop. Otherwise, a non-nil reocrd
// will be contained combining the provided values.
func UnmarshalMPP(reqMPP *lnrpc.MPPRecord) (*record.MPP, error) {
	// If no MPP record was submitted, assume the user wants to send a
	// regular payment.
	if reqMPP == nil {
		return nil, nil
	}

	reqTotal := reqMPP.TotalAmtMAtoms
	reqAddr := reqMPP.PaymentAddr

	switch {

	// No MPP fields were provided.
	case reqTotal == 0 && len(reqAddr) == 0:
		return nil, fmt.Errorf("missing total_msat and payment_addr")

	// Total is present, but payment address is missing.
	case reqTotal > 0 && len(reqAddr) == 0:
		return nil, fmt.Errorf("missing payment_addr")

	// Payment address is present, but total is missing.
	case reqTotal == 0 && len(reqAddr) > 0:
		return nil, fmt.Errorf("missing total_m_atoms")
	}

	addr, err := lntypes.MakeHash(reqAddr)
	if err != nil {
		return nil, fmt.Errorf("unable to parse "+
			"payment_addr: %v", err)
	}

	total := lnwire.MilliAtom(reqTotal)

	return record.NewMPP(total, addr), nil
}

// MarshalHTLCAttempt constructs an RPC HTLCAttempt from the db representation.
func (r *RouterBackend) MarshalHTLCAttempt(
	htlc channeldb.HTLCAttempt) (*lnrpc.HTLCAttempt, error) {

	var (
		status      lnrpc.HTLCAttempt_HTLCStatus
		resolveTime int64
	)

	switch {
	case htlc.Settle != nil:
		status = lnrpc.HTLCAttempt_SUCCEEDED
		resolveTime = MarshalTimeNano(htlc.Settle.SettleTime)

	case htlc.Failure != nil:
		status = lnrpc.HTLCAttempt_FAILED
		resolveTime = MarshalTimeNano(htlc.Failure.FailTime)

	default:
		status = lnrpc.HTLCAttempt_IN_FLIGHT
	}

	route, err := r.MarshallRoute(&htlc.Route)
	if err != nil {
		return nil, err
	}

	return &lnrpc.HTLCAttempt{
		Status:        status,
		Route:         route,
		AttemptTimeNs: MarshalTimeNano(htlc.AttemptTime),
		ResolveTimeNs: resolveTime,
	}, nil
}

// MarshalTimeNano converts a time.Time into its nanosecond representation. If
// the time is zero, this method simply returns 0, since calling UnixNano() on a
// zero-valued time is undefined.
func MarshalTimeNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
