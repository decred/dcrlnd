package lnwallet

import (
	"errors"
	"net"
	"sync"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwallet/chanfunding"
	"github.com/decred/dcrlnd/lnwire"
)

// CommitmentType is an enum indicating the commitment type we should use for
// the channel we are opening.
type CommitmentType int

const (
	// CommitmentTypeLegacy is the legacy commitment format with a tweaked
	// to_remote key.
	CommitmentTypeLegacy = iota

	// CommitmentTypeTweakless is a newer commitment format where the
	// to_remote key is static.
	CommitmentTypeTweakless

	// CommitmentTypeAnchorsZeroFeeHtlcTx is a commitment type that is an
	// extension of the outdated CommitmentTypeAnchors, which in addition
	// requires second-level HTLC transactions to be signed using a
	// zero-fee.
	CommitmentTypeAnchorsZeroFeeHtlcTx

	// CommitmentTypeScriptEnforcedLease is a commitment type that builds
	// upon CommitmentTypeTweakless and CommitmentTypeAnchorsZeroFeeHtlcTx,
	// which in addition requires a CLTV clause to spend outputs paying to
	// the channel initiator. This is intended for use on leased channels to
	// guarantee that the channel initiator has no incentives to close a
	// leased channel before its maturity date.
	CommitmentTypeScriptEnforcedLease
)

// HasStaticRemoteKey returns whether the commitment type supports remote
// outputs backed by static keys.
func (c CommitmentType) HasStaticRemoteKey() bool {
	switch c {
	case CommitmentTypeTweakless,
		CommitmentTypeAnchorsZeroFeeHtlcTx,
		CommitmentTypeScriptEnforcedLease:
		return true
	default:
		return false
	}
}

// HasAnchors returns whether the commitment type supports anchor outputs.
func (c CommitmentType) HasAnchors() bool {
	switch c {
	case CommitmentTypeAnchorsZeroFeeHtlcTx,
		CommitmentTypeScriptEnforcedLease:
		return true
	default:
		return false
	}
}

// String returns the name of the CommitmentType.
func (c CommitmentType) String() string {
	switch c {
	case CommitmentTypeLegacy:
		return "legacy"
	case CommitmentTypeTweakless:
		return "tweakless"
	case CommitmentTypeAnchorsZeroFeeHtlcTx:
		return "anchors-zero-fee-second-level"
	case CommitmentTypeScriptEnforcedLease:
		return "script-enforced-lease"
	default:
		return "invalid"
	}
}

// ChannelContribution is the primary constituent of the funding workflow
// within lnwallet. Each side first exchanges their respective contributions
// along with channel specific parameters like the min fee/kB. Once
// contributions have been exchanged, each side will then produce signatures
// for all their inputs to the funding transactions, and finally a signature
// for the other party's version of the commitment transaction.
type ChannelContribution struct {
	// FundingOutpoint is the amount of funds contributed to the funding
	// transaction.
	FundingAmount dcrutil.Amount

	// Inputs to the funding transaction.
	Inputs []*wire.TxIn

	// ChangeOutputs are the Outputs to be used in the case that the total
	// value of the funding inputs is greater than the total potential
	// channel capacity.
	ChangeOutputs []*wire.TxOut

	// FirstCommitmentPoint is the first commitment point that will be used
	// to create the revocation key in the first commitment transaction we
	// send to the remote party.
	FirstCommitmentPoint *secp256k1.PublicKey

	// ChannelConfig is the concrete contribution that this node is
	// offering to the channel. This includes all the various constraints
	// such as the min HTLC, and also all the keys which will be used for
	// the duration of the channel.
	*channeldb.ChannelConfig

	// UpfrontShutdown is an optional address to which the channel should be
	// paid out to on cooperative close.
	UpfrontShutdown lnwire.DeliveryAddress
}

// toChanConfig returns the raw channel configuration generated by a node's
// contribution to the channel.
func (c *ChannelContribution) toChanConfig() channeldb.ChannelConfig {
	return *c.ChannelConfig
}

// ChannelReservation represents an intent to open a lightning payment channel
// with a counterparty. The funding processes from reservation to channel opening
// is a 3-step process. In order to allow for full concurrency during the
// reservation workflow, resources consumed by a contribution are "locked"
// themselves. This prevents a number of race conditions such as two funding
// transactions double-spending the same input. A reservation can also be
// canceled, which removes the resources from limbo, allowing another
// reservation to claim them.
//
// The reservation workflow consists of the following three steps:
//  1. lnwallet.InitChannelReservation
//     * One requests the wallet to allocate the necessary resources for a
//     channel reservation. These resources are put in limbo for the lifetime
//     of a reservation.
//     * Once completed the reservation will have the wallet's contribution
//     accessible via the .OurContribution() method. This contribution
//     contains the necessary items to allow the remote party to build both
//     the funding, and commitment transactions.
//  2. ChannelReservation.ProcessContribution/ChannelReservation.ProcessSingleContribution
//     * The counterparty presents their contribution to the payment channel.
//     This allows us to build the funding, and commitment transactions
//     ourselves.
//     * We're now able to sign our inputs to the funding transactions, and
//     the counterparty's version of the commitment transaction.
//     * All signatures crafted by us, are now available via .OurSignatures().
//  3. ChannelReservation.CompleteReservation/ChannelReservation.CompleteReservationSingle
//     * The final step in the workflow. The counterparty presents the
//     signatures for all their inputs to the funding transaction, as well
//     as a signature to our version of the commitment transaction.
//     * We then verify the validity of all signatures before considering the
//     channel "open".
type ChannelReservation struct {
	// This mutex MUST be held when either reading or modifying any of the
	// fields below.
	sync.RWMutex

	// fundingTx is the funding transaction for this pending channel.
	fundingTx *wire.MsgTx

	// In order of sorted inputs. Sorting is done in accordance
	// to BIP-69: https://github.com/bitcoin/bips/blob/master/bip-0069.mediawiki.
	ourFundingInputScripts   []*input.Script
	theirFundingInputScripts []*input.Script

	// Our signature for their version of the commitment transaction.
	ourCommitmentSig   input.Signature
	theirCommitmentSig input.Signature

	ourContribution   *ChannelContribution
	theirContribution *ChannelContribution

	partialState *channeldb.OpenChannel
	nodeAddr     net.Addr

	// The ID of this reservation, used to uniquely track the reservation
	// throughout its lifetime.
	reservationID uint64

	// pendingChanID is the pending channel ID for this channel as
	// identified within the wire protocol.
	pendingChanID [32]byte

	// pushMAtoms the amount of milli-atoms that should be pushed to the
	// responder of a single funding channel as part of the initial
	// commitment state.
	pushMAtoms lnwire.MilliAtom

	wallet     *LightningWallet
	chanFunder chanfunding.Assembler

	fundingIntent chanfunding.Intent

	// nextRevocationKeyLoc stores the key locator information for this
	// channel.
	nextRevocationKeyLoc keychain.KeyLocator
}

// NewChannelReservation creates a new channel reservation. This function is
// used only internally by lnwallet. In order to concurrent safety, the
// creation of all channel reservations should be carried out via the
// lnwallet.InitChannelReservation interface.
func NewChannelReservation(capacity, localFundingAmt dcrutil.Amount,
	commitFeePerKB chainfee.AtomPerKByte, wallet *LightningWallet,
	id uint64, pushMAtoms lnwire.MilliAtom, chainHash *chainhash.Hash,
	flags lnwire.FundingFlag, commitType CommitmentType,
	fundingAssembler chanfunding.Assembler,
	pendingChanID [32]byte, thawHeight uint32) (*ChannelReservation, error) {

	var (
		ourBalance   lnwire.MilliAtom
		theirBalance lnwire.MilliAtom
		initiator    bool
	)

	// Based on the channel type, we determine the initial commit size and
	// fee.
	commitSize := input.CommitmentTxSize
	if commitType.HasAnchors() {
		commitSize = input.CommitmentWithAnchorsTxSize
	}
	commitFee := commitFeePerKB.FeeForSize(commitSize)

	localFundingMAtoms := lnwire.NewMAtomsFromAtoms(localFundingAmt)
	// TODO(halseth): make method take remote funding amount directly
	// instead of inferring it from capacity and local amt.
	capacityMAtoms := lnwire.NewMAtomsFromAtoms(capacity)

	// The total fee paid by the initiator will be the commitment fee in
	// addition to the two anchor outputs.
	feeMAtoms := lnwire.NewMAtomsFromAtoms(commitFee)
	if commitType.HasAnchors() {
		feeMAtoms += 2 * lnwire.NewMAtomsFromAtoms(anchorSize)
	}

	// Used to cut down on verbosity.
	defaultDust := wallet.Cfg.DefaultConstraints.DustLimit

	// If we're the responder to a single-funder reservation, then we have
	// no initial balance in the channel unless the remote party is pushing
	// some funds to us within the first commitment state.
	if localFundingAmt == 0 {
		ourBalance = pushMAtoms
		theirBalance = capacityMAtoms - feeMAtoms - pushMAtoms
		initiator = false

		// If the responder doesn't have enough funds to actually pay
		// the fees, then we'll bail our early.
		if int64(theirBalance) < 0 {
			return nil, ErrFunderBalanceDust(
				int64(commitFee), int64(theirBalance.ToAtoms()),
				int64(2*defaultDust),
			)
		}
	} else {
		// TODO(roasbeef): need to rework fee structure in general and
		// also when we "unlock" dual funder within the daemon

		if capacity == localFundingAmt {
			// If we're initiating a single funder workflow, then
			// we pay all the initial fees within the commitment
			// transaction. We also deduct our balance by the
			// amount pushed as part of the initial state.
			ourBalance = capacityMAtoms - feeMAtoms - pushMAtoms
			theirBalance = pushMAtoms
		} else {
			// Otherwise, this is a dual funder workflow where both
			// slides split the amount funded and the commitment
			// fee.
			ourBalance = localFundingMAtoms - (feeMAtoms / 2)
			theirBalance = capacityMAtoms - localFundingMAtoms - (feeMAtoms / 2) + pushMAtoms
		}

		initiator = true

		// If we, the initiator don't have enough funds to actually pay
		// the fees, then we'll exit with an error.
		if int64(ourBalance) < 0 {
			return nil, ErrFunderBalanceDust(
				int64(commitFee), int64(ourBalance),
				int64(2*defaultDust),
			)
		}
	}

	// If we're the initiator and our starting balance within the channel
	// after we take account of fees is below 2x the dust limit, then we'll
	// reject this channel creation request.
	//
	// TODO(roasbeef): reject if 30% goes to fees? dust channel
	if initiator && ourBalance.ToAtoms() <= 2*defaultDust {
		return nil, ErrFunderBalanceDust(
			int64(commitFee),
			int64(ourBalance.ToAtoms()),
			int64(2*defaultDust),
		)
	}

	// Similarly we ensure their balance is reasonable if we are not the
	// initiator.
	if !initiator && theirBalance.ToAtoms() <= 2*defaultDust {
		return nil, ErrFunderBalanceDust(
			int64(commitFee),
			int64(theirBalance.ToAtoms()),
			int64(2*defaultDust),
		)
	}

	// Next we'll set the channel type based on what we can ascertain about
	// the balances/push amount within the channel.
	var chanType channeldb.ChannelType

	// If either of the balances are zero at this point, or we have a
	// non-zero push amt (there's no pushing for dual funder), then this is
	// a single-funder channel.
	if ourBalance == 0 || theirBalance == 0 || pushMAtoms != 0 {
		// Both the tweakless type and the anchor type is tweakless,
		// hence set the bit.
		if commitType.HasStaticRemoteKey() {
			chanType |= channeldb.SingleFunderTweaklessBit
		} else {
			chanType |= channeldb.SingleFunderBit
		}

		switch a := fundingAssembler.(type) {
		// The first channels of a batch shouldn't publish the batch TX
		// to avoid problems if some of the funding flows can't be
		// completed. Only the last channel of a batch should publish.
		case chanfunding.ConditionalPublishAssembler:
			if !a.ShouldPublishFundingTx() {
				chanType |= channeldb.NoFundingTxBit
			}

		// Normal funding flow, the assembler creates a TX from the
		// internal wallet.
		case chanfunding.FundingTxAssembler:
			// Do nothing, a FundingTxAssembler has the transaction.

		// If this intent isn't one that's able to provide us with a
		// funding transaction, then we'll set the chanType bit to
		// signal that we don't have access to one.
		default:
			chanType |= channeldb.NoFundingTxBit
		}

	} else {
		// Otherwise, this is a dual funder channel, and no side is
		// technically the "initiator"
		initiator = false
		chanType |= channeldb.DualFunderBit
	}

	// We are adding anchor outputs to our commitment. We only support this
	// in combination with zero-fee second-levels HTLCs.
	if commitType.HasAnchors() {
		chanType |= channeldb.AnchorOutputsBit
		chanType |= channeldb.ZeroHtlcTxFeeBit
	}

	// Set the appropriate LeaseExpiration/Frozen bit based on the
	// reservation parameters.
	if commitType == CommitmentTypeScriptEnforcedLease {
		if thawHeight == 0 {
			return nil, errors.New("missing absolute expiration " +
				"for script enforced lease commitment type")
		}
		chanType |= channeldb.LeaseExpirationBit
	} else if thawHeight > 0 {
		chanType |= channeldb.FrozenBit
	}

	return &ChannelReservation{
		ourContribution: &ChannelContribution{
			FundingAmount: ourBalance.ToAtoms(),
			ChannelConfig: &channeldb.ChannelConfig{},
		},
		theirContribution: &ChannelContribution{
			FundingAmount: theirBalance.ToAtoms(),
			ChannelConfig: &channeldb.ChannelConfig{},
		},
		partialState: &channeldb.OpenChannel{
			ChanType:     chanType,
			ChainHash:    *chainHash,
			IsPending:    true,
			IsInitiator:  initiator,
			ChannelFlags: flags,
			Capacity:     capacity,
			LocalCommitment: channeldb.ChannelCommitment{
				LocalBalance:  ourBalance,
				RemoteBalance: theirBalance,
				FeePerKB:      dcrutil.Amount(commitFeePerKB),
				CommitFee:     commitFee,
			},
			RemoteCommitment: channeldb.ChannelCommitment{
				LocalBalance:  ourBalance,
				RemoteBalance: theirBalance,
				FeePerKB:      dcrutil.Amount(commitFeePerKB),
				CommitFee:     commitFee,
			},
			ThawHeight: thawHeight,
			Db:         wallet.Cfg.Database,
		},
		pushMAtoms:    pushMAtoms,
		pendingChanID: pendingChanID,
		reservationID: id,
		wallet:        wallet,
		chanFunder:    fundingAssembler,
	}, nil
}

// SetNumConfsRequired sets the number of confirmations that are required for
// the ultimate funding transaction before the channel can be considered open.
// This is distinct from the main reservation workflow as it allows
// implementations a bit more flexibility w.r.t to if the responder of the
// initiator sets decides the number of confirmations needed.
func (r *ChannelReservation) SetNumConfsRequired(numConfs uint16) {
	r.Lock()
	defer r.Unlock()

	r.partialState.NumConfsRequired = numConfs
}

// CommitConstraints takes the constraints that the remote party specifies for
// the type of commitments that we can generate for them. These constraints
// include several parameters that serve as flow control restricting the amount
// of atoms that can be transferred in a single commitment. This function
// will also attempt to verify the constraints for sanity, returning an error
// if the parameters are seemed unsound.
func (r *ChannelReservation) CommitConstraints(c *channeldb.ChannelConstraints,
	maxLocalCSVDelay uint16, responder bool) error {
	r.Lock()
	defer r.Unlock()

	// Fail if the csv delay for our funds exceeds our maximum.
	if c.CsvDelay > maxLocalCSVDelay {
		return ErrCsvDelayTooLarge(c.CsvDelay, maxLocalCSVDelay)
	}

	// The channel reserve should always be greater or equal to the dust
	// limit. The reservation request should be denied if otherwise.
	if c.DustLimit > c.ChanReserve {
		return ErrChanReserveTooSmall(c.ChanReserve, c.DustLimit)
	}

	// Validate against the maximum-sized pk script dust limit, and
	// also ensure that the DustLimit is not too large.
	maxWitnessLimit := DustLimitForSize(input.P2PKHPkScriptSize)
	if c.DustLimit < maxWitnessLimit || c.DustLimit > 3*maxWitnessLimit {
		return ErrInvalidDustLimit(c.DustLimit)
	}

	// Fail if we consider the channel reserve to be too large.  We
	// currently fail if it is greater than 20% of the channel capacity.
	maxChanReserve := r.partialState.Capacity / 5
	if c.ChanReserve > maxChanReserve {
		return ErrChanReserveTooLarge(c.ChanReserve, maxChanReserve)
	}

	// Fail if the minimum HTLC value is too large. If this is too large,
	// the channel won't be useful for sending small payments. This limit
	// is currently set to maxValueInFlight, effectively letting the remote
	// setting this as large as it wants.
	if c.MinHTLC > c.MaxPendingAmount {
		return ErrMinHtlcTooLarge(c.MinHTLC, c.MaxPendingAmount)
	}

	// Fail if maxHtlcs is above the maximum allowed number of 483.  This
	// number is specified in BOLT-02.
	//
	// FIXME(decred): The MaxHTLCNumber was 322 in previous vesions and was
	// reduced to 300 recently. We add a special case so that we can still
	// open channels to old nodes. This should be removed on the next
	// version.
	if c.MaxAcceptedHtlcs > uint16(input.MaxHTLCNumber/2) && c.MaxAcceptedHtlcs != 161 {
		return ErrMaxHtlcNumTooLarge(
			c.MaxAcceptedHtlcs, uint16(input.MaxHTLCNumber/2),
		)
	}

	// Fail if we consider maxHtlcs too small. If this is too small we
	// cannot offer many HTLCs to the remote.
	const minNumHtlc = 5
	if c.MaxAcceptedHtlcs < minNumHtlc {
		return ErrMaxHtlcNumTooSmall(c.MaxAcceptedHtlcs, minNumHtlc)
	}

	// Fail if we consider maxValueInFlight too small. We currently require
	// the remote to at least allow minNumHtlc * minHtlc in flight.
	if c.MaxPendingAmount < minNumHtlc*c.MinHTLC {
		return ErrMaxValueInFlightTooSmall(
			c.MaxPendingAmount, minNumHtlc*c.MinHTLC,
		)
	}

	// Our dust limit should always be less than or equal to our proposed
	// channel reserve.
	if responder && r.ourContribution.DustLimit > c.ChanReserve {
		r.ourContribution.DustLimit = c.ChanReserve
	}

	r.ourContribution.ChanReserve = c.ChanReserve
	r.ourContribution.MaxPendingAmount = c.MaxPendingAmount
	r.ourContribution.MinHTLC = c.MinHTLC
	r.ourContribution.MaxAcceptedHtlcs = c.MaxAcceptedHtlcs
	r.ourContribution.CsvDelay = c.CsvDelay

	return nil
}

// validateReserveBounds checks that both ChannelReserve values are above both
// DustLimit values. This not only avoids stuck channels, but is also mandated
// by BOLT#02 even if it's not explicit. This returns true if the bounds are
// valid. This function should be called with the lock held.
func (r *ChannelReservation) validateReserveBounds() bool {
	ourDustLimit := r.ourContribution.DustLimit
	ourRequiredReserve := r.ourContribution.ChanReserve
	theirDustLimit := r.theirContribution.DustLimit
	theirRequiredReserve := r.theirContribution.ChanReserve

	// We take the smaller of the two ChannelReserves and compare it
	// against the larger of the two DustLimits.
	minChanReserve := ourRequiredReserve
	if minChanReserve > theirRequiredReserve {
		minChanReserve = theirRequiredReserve
	}

	maxDustLimit := ourDustLimit
	if maxDustLimit < theirDustLimit {
		maxDustLimit = theirDustLimit
	}

	return minChanReserve >= maxDustLimit
}

// OurContribution returns the wallet's fully populated contribution to the
// pending payment channel. See 'ChannelContribution' for further details
// regarding the contents of a contribution.
//
// NOTE: This SHOULD NOT be modified.
// TODO(roasbeef): make copy?
func (r *ChannelReservation) OurContribution() *ChannelContribution {
	r.RLock()
	defer r.RUnlock()

	return r.ourContribution
}

// ProcessContribution verifies the counterparty's contribution to the pending
// payment channel. As a result of this incoming message, lnwallet is able to
// build the funding transaction, and both commitment transactions. Once this
// message has been processed, all signatures to inputs to the funding
// transaction belonging to the wallet are available. Additionally, the wallet
// will generate a signature to the counterparty's version of the commitment
// transaction.
func (r *ChannelReservation) ProcessContribution(theirContribution *ChannelContribution) error {
	errChan := make(chan error, 1)

	r.wallet.msgChan <- &addContributionMsg{
		pendingFundingID: r.reservationID,
		contribution:     theirContribution,
		err:              errChan,
	}

	return <-errChan
}

// IsPsbt returns true if there is a PSBT funding intent mapped to this
// reservation.
func (r *ChannelReservation) IsPsbt() bool {
	_, ok := r.fundingIntent.(*chanfunding.PsbtIntent)
	return ok
}

// IsCannedShim returns true if there is a canned shim funding intent mapped to
// this reservation.
func (r *ChannelReservation) IsCannedShim() bool {
	_, ok := r.fundingIntent.(*chanfunding.ShimIntent)
	return ok
}

// ProcessPsbt continues a previously paused funding flow that involves PSBT to
// construct the funding transaction. This method can be called once the PSBT is
// finalized and the signed transaction is available.
func (r *ChannelReservation) ProcessPsbt() error {
	errChan := make(chan error, 1)

	r.wallet.msgChan <- &continueContributionMsg{
		pendingFundingID: r.reservationID,
		err:              errChan,
	}

	return <-errChan
}

// RemoteCanceled informs the PSBT funding state machine that the remote peer
// has canceled the pending reservation, likely due to a timeout.
func (r *ChannelReservation) RemoteCanceled() {
	psbtIntent, ok := r.fundingIntent.(*chanfunding.PsbtIntent)
	if !ok {
		return
	}
	psbtIntent.RemoteCanceled()
}

// ProcessSingleContribution verifies, and records the initiator's contribution
// to this pending single funder channel. Internally, no further action is
// taken other than recording the initiator's contribution to the single funder
// channel.
func (r *ChannelReservation) ProcessSingleContribution(theirContribution *ChannelContribution) error {
	errChan := make(chan error, 1)

	r.wallet.msgChan <- &addSingleContributionMsg{
		pendingFundingID: r.reservationID,
		contribution:     theirContribution,
		err:              errChan,
	}

	return <-errChan
}

// TheirContribution returns the counterparty's pending contribution to the
// payment channel. See 'ChannelContribution' for further details regarding the
// contents of a contribution. This attribute will ONLY be available after a
// call to .ProcessContribution().
//
// NOTE: This SHOULD NOT be modified.
func (r *ChannelReservation) TheirContribution() *ChannelContribution {
	r.RLock()
	defer r.RUnlock()
	return r.theirContribution
}

// OurSignatures retrieves the wallet's signatures to all inputs to the funding
// transaction belonging to itself, and also a signature for the counterparty's
// version of the commitment transaction. The signatures for the wallet's
// inputs to the funding transaction are returned in sorted order according to
// BIP-69: https://github.com/bitcoin/bips/blob/master/bip-0069.mediawiki.
//
// NOTE: These signatures will only be populated after a call to
// .ProcessContribution()
func (r *ChannelReservation) OurSignatures() ([]*input.Script,
	input.Signature) {

	r.RLock()
	defer r.RUnlock()
	return r.ourFundingInputScripts, r.ourCommitmentSig
}

// CompleteReservation finalizes the pending channel reservation, transitioning
// from a pending payment channel, to an open payment channel. All passed
// signatures to the counterparty's inputs to the funding transaction will be
// fully verified. Signatures are expected to be passed in sorted order
// according to BIP-69:
// https://github.com/bitcoin/bips/blob/master/bip-0069.mediawiki.
// Additionally, verification is performed in order to ensure that the
// counterparty supplied a valid signature to our version of the commitment
// transaction.  Once this method returns, callers should broadcast the
// created funding transaction, then call .WaitForChannelOpen() which will
// block until the funding transaction obtains the configured number of
// confirmations. Once the method unblocks, a LightningChannel instance is
// returned, marking the channel available for updates.
func (r *ChannelReservation) CompleteReservation(fundingInputScripts []*input.Script,
	commitmentSig input.Signature) (*channeldb.OpenChannel, error) {

	// TODO(roasbeef): add flag for watch or not?
	errChan := make(chan error, 1)
	completeChan := make(chan *channeldb.OpenChannel, 1)

	r.wallet.msgChan <- &addCounterPartySigsMsg{
		pendingFundingID:         r.reservationID,
		theirFundingInputScripts: fundingInputScripts,
		theirCommitmentSig:       commitmentSig,
		completeChan:             completeChan,
		err:                      errChan,
	}

	return <-completeChan, <-errChan
}

// CompleteReservationSingle finalizes the pending single funder channel
// reservation. Using the funding outpoint of the constructed funding
// transaction, and the initiator's signature for our version of the commitment
// transaction, we are able to verify the correctness of our commitment
// transaction as crafted by the initiator. Once this method returns, our
// signature for the initiator's version of the commitment transaction is
// available via the .OurSignatures() method. As this method should only be
// called as a response to a single funder channel, only a commitment signature
// will be populated.
func (r *ChannelReservation) CompleteReservationSingle(fundingPoint *wire.OutPoint,
	commitSig input.Signature) (*channeldb.OpenChannel, error) {

	errChan := make(chan error, 1)
	completeChan := make(chan *channeldb.OpenChannel, 1)

	r.wallet.msgChan <- &addSingleFunderSigsMsg{
		pendingFundingID:   r.reservationID,
		fundingOutpoint:    fundingPoint,
		theirCommitmentSig: commitSig,
		completeChan:       completeChan,
		err:                errChan,
	}

	return <-completeChan, <-errChan
}

// TheirSignatures returns the counterparty's signatures to all inputs to the
// funding transaction belonging to them, as well as their signature for the
// wallet's version of the commitment transaction. This methods is provided for
// additional verification, such as needed by tests.
//
// NOTE: These attributes will be unpopulated before a call to
// .CompleteReservation().
func (r *ChannelReservation) TheirSignatures() ([]*input.Script,
	input.Signature) {

	r.RLock()
	defer r.RUnlock()
	return r.theirFundingInputScripts, r.theirCommitmentSig
}

// FinalFundingTx returns the finalized, fully signed funding transaction for
// this reservation.
//
// NOTE: If this reservation was created as the non-initiator to a single
// funding workflow, then the full funding transaction will not be available.
// Instead we will only have the final outpoint of the funding transaction.
func (r *ChannelReservation) FinalFundingTx() *wire.MsgTx {
	r.RLock()
	defer r.RUnlock()
	return r.fundingTx
}

// FundingOutpoint returns the outpoint of the funding transaction.
//
// NOTE: The pointer returned will only be set once the .ProcessContribution()
// method is called in the case of the initiator of a single funder workflow,
// and after the .CompleteReservationSingle() method is called in the case of
// a responder to a single funder workflow.
func (r *ChannelReservation) FundingOutpoint() *wire.OutPoint {
	r.RLock()
	defer r.RUnlock()
	return &r.partialState.FundingOutpoint
}

// SetOurUpfrontShutdown sets the upfront shutdown address on our contribution.
func (r *ChannelReservation) SetOurUpfrontShutdown(shutdown lnwire.DeliveryAddress) {
	r.Lock()
	defer r.Unlock()

	r.ourContribution.UpfrontShutdown = shutdown
}

// Capacity returns the channel capacity for this reservation.
func (r *ChannelReservation) Capacity() dcrutil.Amount {
	r.RLock()
	defer r.RUnlock()
	return r.partialState.Capacity
}

// LeaseExpiry returns the absolute expiration height for a leased channel using
// the script enforced commitment type. A zero value is returned when the
// channel is not using a script enforced lease commitment type.
func (r *ChannelReservation) LeaseExpiry() uint32 {
	if !r.partialState.ChanType.HasLeaseExpiration() {
		return 0
	}
	return r.partialState.ThawHeight
}

// Cancel abandons this channel reservation. This method should be called in
// the scenario that communications with the counterparty break down. Upon
// cancellation, all resources previously reserved for this pending payment
// channel are returned to the free pool, allowing subsequent reservations to
// utilize the now freed resources.
func (r *ChannelReservation) Cancel() error {
	errChan := make(chan error, 1)
	r.wallet.msgChan <- &fundingReserveCancelMsg{
		pendingFundingID: r.reservationID,
		err:              errChan,
	}

	return <-errChan
}

// OpenChannelDetails wraps the finalized fully confirmed channel which
// resulted from a ChannelReservation instance with details concerning exactly
// _where_ in the chain the channel was ultimately opened.
type OpenChannelDetails struct {
	// Channel is the active channel created by an instance of a
	// ChannelReservation and the required funding workflow.
	Channel *LightningChannel

	// ConfirmationHeight is the block height within the chain that
	// included the channel.
	ConfirmationHeight uint32

	// TransactionIndex is the index within the confirming block that the
	// transaction resides.
	TransactionIndex uint32
}
