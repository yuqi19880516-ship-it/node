package keeper

import (
	"bytes"
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	internaltypes "github.com/TrueOpen/node/x/task/internal/types"
	"github.com/TrueOpen/node/x/task/types"
)

// The per-model brake. Per model and settlement period the chain counts the
// selected anchor audits that reached a final verdict (audited) and those of
// them that ended in a REJECT of the Worker's proof (cases). The brake trips
// for the period when cases reaches anchor_brake_min_cases and the period's
// rate is at least anchor_brake_multiple_x1000 / 1000 times the mean rate of
// the preceding anchor_brake_baseline_periods periods that had audits and did
// not trip; without such a period the case count alone trips it. From the
// tripping case on, the Worker slash of a REJECT verdict on that model in that
// period is withheld as a pending effect. Nothing else changes: the task still
// fails, the Worker is still unpaid for it, the user is still refunded.
// MsgReleaseAnchorBrake, signed by the module authority, applies or discards
// the pending effects. The brake is off when anchor_brake_min_cases or
// batch_settlement_period_blocks is zero.

// maxAnchorBrakeEffectsPerRelease bounds the work of one release message; a
// release with more pending effects is repeated until none is left.
const maxAnchorBrakeEffectsPerRelease = 256

func anchorBrakePeriod(params types.TaskParamsV1, height uint64) (uint64, bool) {
	if params.Verification.AnchorBrakeMinCases == 0 {
		return 0, false
	}
	return hubtypes.WorkerSettlementPeriod(height, params.Verification.BatchSettlementPeriodBlocks)
}

func anchorBrakeRateBps(cases, audited uint32) uint32 {
	if audited == 0 {
		return 0
	}
	return uint32(uint64(cases) * uint64(types.BasisPointsMaximum) / uint64(audited))
}

// anchorBrakeTrips is the trip rule on one period's counts and its baseline.
func anchorBrakeTrips(cases, rateBps, baselineBps uint32, hasBaseline bool, minCases, multipleX1000 uint32) bool {
	if minCases == 0 || cases < minCases {
		return false
	}
	if !hasBaseline || baselineBps == 0 {
		return true
	}
	return uint64(rateBps)*1000 >= uint64(multipleX1000)*uint64(baselineBps)
}

func (k Keeper) readAnchorBrake(ctx context.Context, modelID []byte, period uint64) (types.AnchorBrakeState, bool, error) {
	row, err := k.AnchorBrake.Get(ctx, collections.Join(types.Hash32Key(modelID), period))
	if errors.Is(err, collections.ErrNotFound) {
		return types.AnchorBrakeState{ModelId: bytes.Clone(modelID), Period: period}, false, nil
	}
	return row, err == nil, err
}

// anchorBrakeBaseline is the mean REJECT rate of the preceding periods that
// had audits and did not trip.
func (k Keeper) anchorBrakeBaseline(ctx context.Context, modelID []byte, period uint64, periods uint32) (uint32, bool, error) {
	var sum, count uint64
	for back := uint64(1); back <= uint64(periods) && back <= period; back++ {
		row, found, err := k.readAnchorBrake(ctx, modelID, period-back)
		if err != nil {
			return 0, false, err
		}
		if !found || row.Audited == 0 || row.Tripped {
			continue
		}
		sum += uint64(anchorBrakeRateBps(row.Cases, row.Audited))
		count++
	}
	if count == 0 {
		return 0, false, nil
	}
	return uint32(sum / count), true, nil
}

// countAnchorAudit adds one finalised audit to the model's period and trips
// the brake when the rule is met. It returns the row as stored and whether the
// brake is configured at all.
func (k Keeper) countAnchorAudit(ctx context.Context, params types.TaskParamsV1, modelID []byte, height uint64, rejectCase bool) (types.AnchorBrakeState, bool, error) {
	period, on := anchorBrakePeriod(params, height)
	if !on {
		return types.AnchorBrakeState{}, false, nil
	}
	row, _, err := k.readAnchorBrake(ctx, modelID, period)
	if err != nil {
		return row, true, err
	}
	row.Audited++
	if rejectCase {
		row.Cases++
	}
	row.RateBps = anchorBrakeRateBps(row.Cases, row.Audited)
	if rejectCase && !row.Tripped && row.Cases >= params.Verification.AnchorBrakeMinCases {
		baseline, hasBaseline, err := k.anchorBrakeBaseline(ctx, modelID, period, params.Verification.AnchorBrakeBaselinePeriods)
		if err != nil {
			return row, true, err
		}
		if anchorBrakeTrips(row.Cases, row.RateBps, baseline, hasBaseline, params.Verification.AnchorBrakeMinCases, params.Verification.AnchorBrakeMultipleX1000) {
			row.Tripped, row.TrippedHeight, row.BaselineBps = true, height, baseline
			if err := emitTypedEvent(ctx, &types.EventAnchorBrakeTripped{ModelId: bytes.Clone(modelID), Period: period, Cases: row.Cases, RateBps: row.RateBps, BaselineBps: baseline}); err != nil {
				return row, true, err
			}
		}
	}
	return row, true, k.AnchorBrake.Set(ctx, collections.Join(types.Hash32Key(modelID), period), row)
}

// anchorBrakeWithholds counts a REJECT verdict of the Worker's proof and
// decides what happens to its slash: applied as usual (false), or not applied
// now (true). While the period is tripped and not released the slash is kept
// as a pending effect. After a release the later slashes of the period follow
// the release: applied when the pending ones were applied, dropped when they
// were discarded.
func (k Keeper) anchorBrakeWithholds(ctx context.Context, params types.TaskParamsV1, modelID []byte, fact hubtypes.TaskRoleFaultFact, height uint64) (bool, error) {
	row, on, err := k.countAnchorAudit(ctx, params, modelID, height, true)
	if err != nil || !on || !row.Tripped {
		return false, err
	}
	if row.Released {
		return !row.Applied, nil
	}
	operator, err := k.sessionAddressToStore("withheld slash operator", fact.OperatorAddress)
	if err != nil {
		return false, err
	}
	if err := k.AnchorBrakePending.Set(ctx, collections.Join3(types.Hash32Key(modelID), row.Period, types.Hash32Key(fact.TaskID)), internaltypes.AnchorBrakePendingEffect{
		SessionId: bytes.Clone(fact.SessionID), TaskId: bytes.Clone(fact.TaskID), OperatorAddress: operator, FaultType: fact.FaultType,
		EvidenceDigest: bytes.Clone(fact.EvidenceDigest), SlashBps: fact.SlashBps, RequestedAmount: fact.RequestedAmount, WithheldHeight: height,
	}); err != nil {
		return false, err
	}
	row.PendingEffects++
	return true, k.AnchorBrake.Set(ctx, collections.Join(types.Hash32Key(modelID), row.Period), row)
}

// ReleaseAnchorBrake ends a tripped brake of one model and period: the module
// authority applies or discards the withheld Worker slashes. One message
// resolves at most maxAnchorBrakeEffectsPerRelease effects; repeating it with
// the same decision resolves the rest.
func (m msgServer) ReleaseAnchorBrake(ctx context.Context, req *types.MsgReleaseAnchorBrake) (*types.MsgReleaseAnchorBrakeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "release request is required")
	}
	authority, err := m.k.addressCodec.StringToBytes(req.Authority)
	if err != nil || !bytes.Equal(authority, m.k.authority) {
		return nil, status.Error(codes.PermissionDenied, "authority does not match task authority")
	}
	if len(req.ModelId) != types.Hash32Len {
		return nil, status.Error(codes.InvalidArgument, "model_id must be 32 bytes")
	}
	row, found, err := m.k.readAnchorBrake(ctx, req.ModelId, req.Period)
	if err != nil {
		return nil, err
	}
	if !found || !row.Tripped {
		return nil, status.Error(codes.FailedPrecondition, "the brake of this model and period is not tripped")
	}
	if row.Released && row.Applied != req.Apply {
		return nil, status.Error(codes.FailedPrecondition, "the brake was already released with the other decision")
	}
	height, err := currentBlockHeight(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := m.k.resolveAnchorBrakeEffects(ctx, req.ModelId, req.Period, req.Apply, height)
	if err != nil {
		return nil, err
	}
	if row.Released && resolved == 0 {
		return &types.MsgReleaseAnchorBrakeResponse{Status: shared.MutationStatusV1_MUTATION_STATUS_V1_NOOP}, nil
	}
	row.PendingEffects -= min(row.PendingEffects, resolved)
	if !row.Released {
		row.Released, row.Applied, row.ReleasedHeight = true, req.Apply, height
	}
	if err := m.k.AnchorBrake.Set(ctx, collections.Join(types.Hash32Key(req.ModelId), req.Period), row); err != nil {
		return nil, err
	}
	if err := emitTypedEvent(ctx, &types.EventAnchorBrakeReleased{ModelId: bytes.Clone(req.ModelId), Period: req.Period, Applied: req.Apply, ResolvedEffects: resolved}); err != nil {
		return nil, err
	}
	return &types.MsgReleaseAnchorBrakeResponse{ResolvedEffects: resolved, Status: shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED}, nil
}

// resolveAnchorBrakeEffects applies or discards the pending effects of one
// brake. An effect whose slash cannot be applied any more (the operator's bond
// is gone) is removed with the rest: it must not hold the release up.
func (k Keeper) resolveAnchorBrakeEffects(ctx context.Context, modelID []byte, period uint64, apply bool, height uint64) (uint32, error) {
	iter, err := k.AnchorBrakePending.Iterate(ctx, collections.NewSuperPrefixedTripleRange[types.Hash32Key, uint64, types.Hash32Key](types.Hash32Key(modelID), period))
	if err != nil {
		return 0, err
	}
	entries, err := iter.KeyValues()
	iter.Close()
	if err != nil {
		return 0, err
	}
	if len(entries) > maxAnchorBrakeEffectsPerRelease {
		entries = entries[:maxAnchorBrakeEffectsPerRelease]
	}
	for _, entry := range entries {
		if apply {
			if err := k.applyWithheldAnchorSlash(ctx, entry.Value, height); err != nil {
				return 0, err
			}
		}
		if err := k.AnchorBrakePending.Remove(ctx, entry.Key); err != nil {
			return 0, err
		}
	}
	return uint32(len(entries)), nil
}

func (k Keeper) applyWithheldAnchorSlash(ctx context.Context, effect internaltypes.AnchorBrakePendingEffect, height uint64) error {
	operator, err := k.sessionAddressFromStore("withheld slash operator", effect.OperatorAddress)
	if err != nil {
		return err
	}
	cacheCtx, write := sdk.UnwrapSDKContext(ctx).CacheContext()
	applied, err := k.hubKeeper.ApplyTaskRoleFault(sdk.WrapSDKContext(cacheCtx), hubtypes.TaskRoleFaultFact{
		SessionID: effect.SessionId, TaskID: effect.TaskId, OperatorAddress: operator, Duty: shared.DutyWorker, FaultType: effect.FaultType,
		ClassificationSource: shared.FailureClassificationSource_FAILURE_CLASSIFICATION_SOURCE_SETTLEMENT,
		EvidenceDigest:       effect.EvidenceDigest, SlashBps: effect.SlashBps, Height: height, RequestedAmount: effect.RequestedAmount,
	})
	if err != nil {
		return nil
	}
	write()
	audit, err := k.AnchorAudit.Get(ctx, types.NewTaskKey(effect.TaskId))
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	audit.WorkerFaultId = bytes.Clone(applied.FaultId)
	return k.AnchorAudit.Set(ctx, types.NewTaskKey(effect.TaskId), audit)
}

// AnchorBrake returns the brake statistic of one model and period; period 0
// is the current period.
func (q queryServer) AnchorBrake(ctx context.Context, req *types.QueryAnchorBrakeRequest) (*types.QueryAnchorBrakeResponse, error) {
	if req == nil || len(req.ModelId) != types.Hash32Len {
		return nil, status.Error(codes.InvalidArgument, "model_id must be 32 bytes")
	}
	period := req.Period
	if period == 0 {
		params, err := q.k.Params.Get(ctx)
		if err != nil {
			return nil, status.Error(codes.Internal, "task params unavailable")
		}
		height, err := currentBlockHeight(ctx)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		period, _ = hubtypes.WorkerSettlementPeriod(height, params.Verification.BatchSettlementPeriodBlocks)
	}
	row, _, err := q.k.readAnchorBrake(ctx, req.ModelId, period)
	if err != nil {
		return nil, status.Error(codes.Internal, "anchor brake unavailable")
	}
	return &types.QueryAnchorBrakeResponse{Brake: row}, nil
}
