package keeper

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

const brakeTestPeriodBlocks = uint64(1_000)

// withBrake turns the brake on with the recommended rule: three cases, three
// times the baseline, baseline over seven periods.
func withBrake(p *types.TaskParamsV1) {
	p.Verification.AnchorBrakeMinCases = types.RecommendedAnchorBrakeMinCases
	p.Verification.AnchorBrakeMultipleX1000 = types.RecommendedAnchorBrakeMultipleX1000
	p.Verification.AnchorBrakeBaselinePeriods = types.RecommendedAnchorBrakeBaselinePeriods
	p.Verification.BatchSettlementPeriodBlocks = brakeTestPeriodBlocks
}

func TestAnchorBrakeTripRuleRows(t *testing.T) {
	for _, row := range []struct {
		name                    string
		cases, rate, baseline   uint32
		hasBaseline             bool
		minCases, multipleX1000 uint32
		want                    bool
	}{
		{"brake off", 50, 10_000, 0, false, 0, 3_000, false},
		{"below the case count", 2, 10_000, 0, false, 3, 3_000, false},
		{"case count reached, no history", 3, 10_000, 0, false, 3, 3_000, true},
		{"case count reached, history with a zero rate", 3, 100, 0, true, 3, 3_000, true},
		{"rate below three times the baseline", 3, 299, 100, true, 3, 3_000, false},
		{"rate exactly three times the baseline", 3, 300, 100, true, 3, 3_000, true},
		{"rate above three times the baseline", 5, 9_000, 100, true, 3, 3_000, true},
		{"multiple of one and a half", 3, 150, 100, true, 3, 1_500, true},
		{"multiple of one and a half, just below", 3, 149, 100, true, 3, 1_500, false},
		{"no multiple set", 3, 1, 9_000, true, 3, 0, true},
	} {
		require.Equal(t, row.want, anchorBrakeTrips(row.cases, row.rate, row.baseline, row.hasBaseline, row.minCases, row.multipleX1000), row.name)
	}
	require.Equal(t, uint32(0), anchorBrakeRateBps(0, 0))
	require.Equal(t, uint32(10_000), anchorBrakeRateBps(4, 4))
	require.Equal(t, uint32(3_333), anchorBrakeRateBps(1, 3))
}

type brakeFixture struct {
	*anchorOnlyFixture
	model []byte
}

func newBrakeFixture(t *testing.T, configure func(*types.TaskParamsV1)) *brakeFixture {
	t.Helper()
	f := newAnchorOnlyFixture(t, 4_000, true)
	if configure != nil {
		configure(&f.params)
		require.NoError(t, f.params.Validate())
		require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
		// A settlement period also turns the quota release on; the base stub
		// has no quota.
		f.keeper.hubKeeper = settleRecordingHub{HubKeeper: f.keeper.hubKeeper, calls: &[]quotaSettleCall{}}
		f.server = msgServer{k: f.keeper}
	}
	return &brakeFixture{anchorOnlyFixture: f, model: mustCore(t, f).ModelId}
}

func (f *brakeFixture) audits(t *testing.T, height uint64, passes, rejects int) types.AnchorBrakeState {
	t.Helper()
	var row types.AnchorBrakeState
	var err error
	for i := 0; i < passes; i++ {
		row, _, err = f.keeper.countAnchorAudit(f.ctx, f.params, f.model, height, false)
		require.NoError(t, err)
	}
	for i := 0; i < rejects; i++ {
		row, _, err = f.keeper.countAnchorAudit(f.ctx, f.params, f.model, height, true)
		require.NoError(t, err)
	}
	return row
}

func (f *brakeFixture) slashFact(task byte) hubtypes.TaskRoleFaultFact {
	return hubtypes.TaskRoleFaultFact{
		SessionID: bytes32(0x61), TaskID: bytes32(task), OperatorAddress: f.worker, Duty: shared.DutyWorker,
		FaultType: hubtypes.FaultTypeAnchorReject, EvidenceDigest: bytes32(task + 1), SlashBps: 2_500, Height: 1, RequestedAmount: 1_000,
		ClassificationSource: shared.FailureClassificationSource_FAILURE_CLASSIFICATION_SOURCE_SETTLEMENT,
	}
}

func (f *brakeFixture) brakeRows(t *testing.T) []types.AnchorBrakeState {
	t.Helper()
	rows, err := collectMapValues(f.ctx, f.keeper.AnchorBrake)
	require.NoError(t, err)
	return rows
}

// With the brake parameters at zero no audit is counted and no slash is
// withheld, whatever the number of REJECT verdicts.
func TestAnchorBrakeIsOffAtDefaults(t *testing.T) {
	f := newBrakeFixture(t, nil)
	for i := 0; i < 20; i++ {
		withheld, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(byte(i+1)), 5)
		require.NoError(t, err)
		require.False(t, withheld)
	}
	_, on, err := f.keeper.countAnchorAudit(f.ctx, f.params, f.model, 5, false)
	require.NoError(t, err)
	require.False(t, on)
	require.Empty(t, f.brakeRows(t))

	// Either parameter alone leaves it off too.
	f.params.Verification.AnchorBrakeMinCases = 3
	_, on, err = f.keeper.countAnchorAudit(f.ctx, f.params, f.model, 5, true)
	require.NoError(t, err)
	require.False(t, on, "no settlement period")
	f.params.Verification.AnchorBrakeMinCases, f.params.Verification.BatchSettlementPeriodBlocks = 0, brakeTestPeriodBlocks
	_, on, err = f.keeper.countAnchorAudit(f.ctx, f.params, f.model, 5, true)
	require.NoError(t, err)
	require.False(t, on, "no case count")
	require.Empty(t, f.brakeRows(t))
}

func TestAnchorBrakeCountsAndTrips(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	// Period 0: 2 REJECT out of 100 audits. Below the case count, no trip.
	row := f.audits(t, 10, 98, 2)
	require.Equal(t, types.AnchorBrakeState{ModelId: f.model, Period: 0, Audited: 100, Cases: 2, RateBps: 200}, row)

	// Period 1: 5 REJECT out of 100. The baseline is period 0's 2%; 5% is
	// below three times that, so five cases do not trip it.
	row = f.audits(t, brakeTestPeriodBlocks+1, 95, 5)
	require.False(t, row.Tripped)
	require.Equal(t, uint32(500), row.RateBps)

	// Period 2: the baseline is the mean of 2% and 5%, 3.5%. Ten REJECT in
	// ninety audits (11.1%) trips at the case that crosses 10.5%.
	row = f.audits(t, 2*brakeTestPeriodBlocks+1, 80, 9)
	require.False(t, row.Tripped, "9 of 89 is 10.11%%, below 10.5%%")
	row = f.audits(t, 2*brakeTestPeriodBlocks+2, 0, 1)
	require.True(t, row.Tripped)
	require.Equal(t, uint32(350), row.BaselineBps)
	require.Equal(t, uint32(1_111), row.RateBps)
	require.Equal(t, 2*brakeTestPeriodBlocks+2, row.TrippedHeight)

	// Period 3: the tripped period 2 is left out of the baseline (still 3.5%).
	row = f.audits(t, 3*brakeTestPeriodBlocks+1, 96, 4)
	require.False(t, row.Tripped, "4%% against a 3.5%% baseline")

	// Another model is counted on its own; without history three cases trip.
	other := bytes32(0xab)
	var otherRow types.AnchorBrakeState
	var err error
	for i := 0; i < 3; i++ {
		otherRow, _, err = f.keeper.countAnchorAudit(f.ctx, f.params, other, 3*brakeTestPeriodBlocks+1, true)
		require.NoError(t, err)
		require.Equal(t, i == 2, otherRow.Tripped)
	}
}

// From the tripping case on, a REJECT slash of that model and period is kept
// as a pending effect. The cases before the trip were slashed as usual.
func TestAnchorBrakeWithholdsFromTheTrippingCase(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	for task := byte(1); task <= 5; task++ {
		withheld, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(task*2), 7)
		require.NoError(t, err)
		require.Equal(t, task >= 3, withheld, "case %d", task)
	}
	row, found, err := f.keeper.readAnchorBrake(f.ctx, f.model, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(5), row.Cases)
	require.Equal(t, uint32(3), row.PendingEffects)

	// The next period starts untripped. Its baseline has no usable period (the
	// only earlier one tripped), so the case count alone decides again.
	withheld, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(40), brakeTestPeriodBlocks+1)
	require.NoError(t, err)
	require.False(t, withheld)

	// A PASS or a timeout is counted and never withheld.
	before := len(*f.faults)
	_, _, err = f.keeper.countAnchorAudit(f.ctx, f.params, f.model, 8, false)
	require.NoError(t, err)
	require.Len(t, *f.faults, before)
}

func (f *brakeFixture) release(t *testing.T, period uint64, apply bool, height int64) (*types.MsgReleaseAnchorBrakeResponse, error) {
	t.Helper()
	authority, err := f.keeper.addressCodec.BytesToString(f.keeper.authority)
	require.NoError(t, err)
	return f.server.ReleaseAnchorBrake(f.at(height), &types.MsgReleaseAnchorBrake{Authority: authority, ModelId: f.model, Period: period, Apply: apply})
}

func (f *brakeFixture) trip(t *testing.T) {
	t.Helper()
	for task := byte(1); task <= 4; task++ {
		_, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(task*2), 7)
		require.NoError(t, err)
	}
}

func TestReleaseAnchorBrakeAppliesTheWithheldSlashes(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	f.trip(t)
	require.Empty(t, *f.faults, "nothing was slashed while the effects were pending")

	response, err := f.release(t, 0, true, 50)
	require.NoError(t, err)
	require.Equal(t, uint32(2), response.ResolvedEffects)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, response.Status)
	require.Len(t, *f.faults, 2)
	for i, fault := range *f.faults {
		want := f.slashFact(byte(6 + 2*i))
		want.Height = 50
		require.Equal(t, want, fault, "the slash is applied with what it was withheld with")
	}
	row, _, err := f.keeper.readAnchorBrake(f.ctx, f.model, 0)
	require.NoError(t, err)
	require.True(t, row.Released)
	require.True(t, row.Applied)
	require.Zero(t, row.PendingEffects)
	require.Equal(t, uint64(50), row.ReleasedHeight)

	// A later REJECT of the same period is slashed as usual again.
	withheld, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(60), 60)
	require.NoError(t, err)
	require.False(t, withheld)

	// Repeating the release is a no-op; the opposite decision is refused.
	response, err = f.release(t, 0, true, 61)
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_NOOP, response.Status)
	_, err = f.release(t, 0, false, 61)
	require.ErrorContains(t, err, "other decision")
	require.Len(t, *f.faults, 2)
}

func TestReleaseAnchorBrakeDiscardsTheWithheldSlashes(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	f.trip(t)
	response, err := f.release(t, 0, false, 50)
	require.NoError(t, err)
	require.Equal(t, uint32(2), response.ResolvedEffects)
	require.Empty(t, *f.faults, "a discarded effect slashes nobody")
	row, _, err := f.keeper.readAnchorBrake(f.ctx, f.model, 0)
	require.NoError(t, err)
	require.True(t, row.Released)
	require.False(t, row.Applied)
	require.Zero(t, row.PendingEffects)

	// A later REJECT of the same period follows the decision: no slash, and
	// nothing left pending.
	withheld, err := f.keeper.anchorBrakeWithholds(f.ctx, f.params, f.model, f.slashFact(60), 60)
	require.NoError(t, err)
	require.True(t, withheld)
	pending, err := collectMapValues(f.ctx, f.keeper.AnchorBrakePending)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestReleaseAnchorBrakeRefusals(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	_, err := f.release(t, 0, true, 50)
	require.ErrorContains(t, err, "not tripped", "no row")
	f.audits(t, 7, 5, 2)
	_, err = f.release(t, 0, true, 50)
	require.ErrorContains(t, err, "not tripped", "counted but not tripped")

	f.trip(t)
	_, err = f.server.ReleaseAnchorBrake(f.at(50), &types.MsgReleaseAnchorBrake{Authority: f.worker, ModelId: f.model, Apply: true})
	require.ErrorContains(t, err, "authority")
	authority, err := f.keeper.addressCodec.BytesToString(f.keeper.authority)
	require.NoError(t, err)
	_, err = f.server.ReleaseAnchorBrake(f.at(50), &types.MsgReleaseAnchorBrake{Authority: authority, ModelId: []byte{1, 2}, Apply: true})
	require.ErrorContains(t, err, "model_id")
	_, err = f.server.ReleaseAnchorBrake(f.at(50), nil)
	require.Error(t, err)
	_, err = f.release(t, 1, true, 50)
	require.ErrorContains(t, err, "not tripped", "another period")
}

func TestAnchorBrakeQuery(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	f.audits(t, brakeTestPeriodBlocks+5, 3, 1)
	query := queryServer{k: f.keeper}
	current, err := query.AnchorBrake(f.at(int64(brakeTestPeriodBlocks+9)), &types.QueryAnchorBrakeRequest{ModelId: f.model})
	require.NoError(t, err)
	require.Equal(t, types.AnchorBrakeState{ModelId: f.model, Period: 1, Audited: 4, Cases: 1, RateBps: 2_500}, current.Brake)
	explicit, err := query.AnchorBrake(f.at(5), &types.QueryAnchorBrakeRequest{ModelId: f.model, Period: 1})
	require.NoError(t, err)
	require.Equal(t, current.Brake, explicit.Brake)
	empty, err := query.AnchorBrake(f.at(5), &types.QueryAnchorBrakeRequest{ModelId: f.model, Period: 9})
	require.NoError(t, err)
	require.Equal(t, types.AnchorBrakeState{ModelId: f.model, Period: 9}, empty.Brake)
	_, err = query.AnchorBrake(f.at(5), &types.QueryAnchorBrakeRequest{ModelId: []byte{1}})
	require.Error(t, err)
	_, err = query.AnchorBrake(f.at(5), nil)
	require.Error(t, err)
}

// rejectAndSettle drives the fixture's task to a validator REJECT of the
// Worker's proof and settles it.
func (f *brakeFixture) rejectAndSettle(t *testing.T) (types.TaskSettlementState, uint64) {
	t.Helper()
	f.submitReceipt(t)
	state := f.sampleRound(t)
	require.True(t, state.Selected)
	f.openAndProveSampled(t, state)
	voteAt := state.SampleHeight + 3
	_, err := f.vote(0, f.valAcct[0], types.AnchorVerdictV1_ANCHOR_VERDICT_V1_REJECT, voteAt)
	require.NoError(t, err)
	_, err = f.vote(1, f.valAcct[1], types.AnchorVerdictV1_ANCHOR_VERDICT_V1_REJECT, voteAt)
	require.NoError(t, err)
	settlement, err := f.settleNow(t, voteAt+1)
	require.NoError(t, err)
	return settlement, voteAt + 1
}

// End to end: the task's REJECT is the third case of its model in the period,
// so it trips the brake. The task fails and the Worker is not paid, exactly as
// without the brake; only the slash waits for the release.
func TestTrippedBrakeWithholdsTheSlashAtSettlement(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	settledPeriod := uint64(0)
	require.NoError(t, f.keeper.AnchorBrake.Set(f.ctx, collections.Join(types.Hash32Key(f.model), settledPeriod),
		types.AnchorBrakeState{ModelId: f.model, Period: settledPeriod, Audited: 2, Cases: 2, RateBps: 10_000}))
	settlement, height := f.rejectAndSettle(t)
	require.Less(t, height, brakeTestPeriodBlocks)

	require.Equal(t, types.TaskVerdict_TASK_VERDICT_FAIL, settlement.Verdict)
	require.Equal(t, types.TaskFailureClass_TASK_FAILURE_CLASS_WORKER_EVIDENCE_FAULT, settlement.FailureClass)
	require.Equal(t, shared.NewAmount(0), settlement.WorkerNet)
	require.Equal(t, shared.NewAmount(f.reserved), settlement.RefundAmount)
	require.Empty(t, *f.faults, "the slash is withheld")
	audit, err := f.keeper.AnchorAudit.Get(f.ctx, f.taskKey)
	require.NoError(t, err)
	require.Empty(t, audit.WorkerFaultId)
	row, _, err := f.keeper.readAnchorBrake(f.ctx, f.model, settledPeriod)
	require.NoError(t, err)
	require.True(t, row.Tripped)
	require.Equal(t, uint32(3), row.Cases)
	require.Equal(t, uint32(1), row.PendingEffects)

	response, err := f.release(t, settledPeriod, true, int64(height+10))
	require.NoError(t, err)
	require.Equal(t, uint32(1), response.ResolvedEffects)
	require.Len(t, *f.faults, 1)
	fault := (*f.faults)[0]
	require.Equal(t, f.worker, fault.OperatorAddress)
	require.Equal(t, hubtypes.FaultTypeAnchorReject, fault.FaultType)
	require.Equal(t, anchorSlashAmount(4_000, f.params.Verification.AnchorRejectSlashBps), fault.RequestedAmount)
	audit, err = f.keeper.AnchorAudit.Get(f.ctx, f.taskKey)
	require.NoError(t, err)
	require.Len(t, audit.WorkerFaultId, types.Hash32Len, "the audit row names the fault once it is applied")
}

// The same REJECT below the case count is slashed at settlement as before,
// and is counted.
func TestUntrippedBrakeLeavesTheSlashAtSettlement(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	settlement, _ := f.rejectAndSettle(t)
	f.requireWorkerFaulted(t, settlement, hubtypes.FaultTypeAnchorReject, f.params.Verification.AnchorRejectSlashBps, 4_000)
	require.Equal(t, []types.AnchorBrakeState{{ModelId: f.model, Period: 0, Audited: 1, Cases: 1, RateBps: 10_000}}, f.brakeRows(t))
}

// A selected audit that passes is counted in the model's period.
func TestPassedAuditIsCountedForTheBrake(t *testing.T) {
	f := newBrakeFixture(t, withBrake)
	f.submitReceipt(t)
	state := f.sampleRound(t)
	require.True(t, state.Selected)
	f.openAndProveSampled(t, state)
	voteAt := state.SampleHeight + 3
	_, err := f.vote(0, f.valAcct[0], types.AnchorVerdictV1_ANCHOR_VERDICT_V1_PASS, voteAt)
	require.NoError(t, err)
	_, err = f.vote(1, f.valAcct[1], types.AnchorVerdictV1_ANCHOR_VERDICT_V1_PASS, voteAt)
	require.NoError(t, err)
	_, err = f.settleNow(t, voteAt+1)
	require.NoError(t, err)
	require.Equal(t, []types.AnchorBrakeState{{ModelId: f.model, Period: 0, Audited: 1}}, f.brakeRows(t))
}

// With the brake off the REJECT settlement writes no brake row.
func TestRejectSettlementWritesNoBrakeRowAtDefaults(t *testing.T) {
	f := newBrakeFixture(t, nil)
	settlement, _ := f.rejectAndSettle(t)
	f.requireWorkerFaulted(t, settlement, hubtypes.FaultTypeAnchorReject, f.params.Verification.AnchorRejectSlashBps, 4_000)
	require.Empty(t, f.brakeRows(t))
}
