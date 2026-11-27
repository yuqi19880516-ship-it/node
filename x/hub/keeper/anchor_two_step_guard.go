package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"

	"github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
)

// requireAnchorTwoStepReady refuses the registration of a Profile version
// whose anchor audit uses two-step sampling (anchor commitment schema version
// 2) on a chain that cannot run it:
//
//   - the positions of such an audit are drawn from proposer-VRF beacons only,
//     so the VRF beacon must already be required at this height. On a chain
//     that still accepts the block-hash placeholder no draw could ever be made;
//   - the current Task parameters must carry the list window and the beacon
//     count, which every task of the Profile freezes at acceptance.
//
// A Profile version of schema version 1, or without an anchor requirement, is
// not affected.
func (k Keeper) requireAnchorTwoStepReady(ctx context.Context, schema shared.EvidenceSchemaV1, height uint64) error {
	twoStep, _ := shared.AnchorTwoStepSamplingRequired(schema)
	if !twoStep {
		return nil
	}
	required, err := k.BeaconSentinelRequiredAtHeight(ctx, height)
	if err != nil {
		return err
	}
	if !required {
		return fmt.Errorf("a Profile with anchor commitment schema version %d needs two-step sampling, which draws from proposer-VRF beacons: beacon.vrf_required_from_height must be set and reached",
			shared.WorkerAnchorCommitmentSchemaVersionV2)
	}
	gate, ok := k.roleFaultConsumers.(types.AnchorTwoStepParamsGate)
	if !ok {
		return fmt.Errorf("a Profile with anchor commitment schema version %d cannot be registered: the Task parameters of two-step sampling cannot be read",
			shared.WorkerAnchorCommitmentSchemaVersionV2)
	}
	configured, err := gate.AnchorTwoStepSamplingConfigured(ctx)
	if err != nil {
		return err
	}
	if !configured {
		return fmt.Errorf("a Profile with anchor commitment schema version %d needs verification.anchor_list_window_blocks and verification.anchor_position_beacon_blocks to be set",
			shared.WorkerAnchorCommitmentSchemaVersionV2)
	}
	return nil
}

// validateVrfRequirementKeptForTwoStep refuses a Hub parameter update that
// takes the VRF beacon requirement away - beacon.vrf_required_from_height set
// to zero, or moved above the current height once it has been reached - while
// a Profile version that is not delisted asks for two-step anchor sampling.
//
// The registration guard (requireAnchorTwoStepReady) checks the requirement
// once, when such a Profile is registered. Without this check a later update
// could remove it: from then on no position draw can be made and every
// selected audit of the Profile ends NO_VERDICT - the Workers are paid and no
// task of the Profile is audited at all. The audit states that outcome with
// its own reason, but it should not be reachable by an oversight. Governance
// that does want to drop the requirement delists the two-step Profile
// versions first.
//
// The Profile table is walked only when an update actually removes a reached
// requirement, which is a rare governance action.
func (k Keeper) validateVrfRequirementKeptForTwoStep(ctx context.Context, current, next types.HubParamsV2, height uint64) error {
	reached := func(p types.HubParamsV2) bool {
		return p.Beacon.VrfRequiredFromHeight != 0 && height >= p.Beacon.VrfRequiredFromHeight
	}
	if !reached(current) || reached(next) {
		return nil
	}
	var blocking *types.ProfileState
	if err := k.Profile.Walk(ctx, nil, func(_ types.ProfileStateKeyPair, profile types.ProfileState) (bool, error) {
		if profile.Status == types.ModelStatusDelisted {
			return false, nil
		}
		if twoStep, _ := shared.AnchorTwoStepSamplingRequired(profile.VerificationProfile.EvidenceSchema); twoStep {
			blocking = &profile
			return true, nil
		}
		return false, nil
	}); err != nil {
		return err
	}
	if blocking == nil {
		return nil
	}
	return errorsmod.Wrapf(types.ErrInvalidBeacon,
		"beacon.vrf_required_from_height cannot be removed or postponed while a Profile uses two-step anchor sampling (model %x version %d): its position draw needs proposer-VRF beacons; delist such Profile versions first",
		blocking.ModelId, blocking.ProfileVersion)
}
