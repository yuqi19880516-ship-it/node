package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

// newBrakedBatchFixture is the batch fixture with a per-model brake that the
// first rejection of a period trips.
func newBrakedBatchFixture(t *testing.T) *batchFixture {
	t.Helper()
	log := &[]panelBankTransfer{}
	panel := newTunedAnchorPanelFixture(t, 4_000, 6, func(params *types.TaskParamsV1) {
		lateParams(lateFreeCount)(params)
		params.Verification.BatchSettlementMaxWorkersPerBlock = batchMaxPerBlock
		params.Verification.AnchorBrakeMinCases = 1
		params.Verification.AnchorBrakeMultipleX1000 = types.RecommendedAnchorBrakeMultipleX1000
		params.Verification.AnchorBrakeBaselinePeriods = types.RecommendedAnchorBrakeBaselinePeriods
	}, func(base *anchorOnlyFixture) {
		require.NoError(t, base.keeper.joinWorkerBatch(base.ctx, base.taskKey, base.worker, base.receiptAt, base.params))
	})
	panel.keeper.bankKeeper = panelRecordingBank{BankKeeper: panel.keeper.bankKeeper, log: log}
	panel.server = msgServer{k: panel.keeper}
	return &batchFixture{anchorPanelFixture: panel, log: log}
}

// rejectUnderBrake settles the fixture task on a panel rejection while the
// brake is tripped and returns the brake period.
func (f *batchFixture) rejectUnderBrake(t *testing.T) uint64 {
	t.Helper()
	f.addMember(t, 0xb1, 100)
	f.openAndProveSampled(t, f.audit)
	f.reload(t)
	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, voteReject, voteReject)
	audit := f.reload(t)
	f.sweep(t, audit.PublicationEndHeight)
	settledAt := audit.PublicationEndHeight + 1
	settlement, err := f.settleNow(t, settledAt)
	require.NoError(t, err)
	require.Equal(t, types.TaskVerdict_TASK_VERDICT_FAIL, settlement.Verdict, "the task fails whatever the brake does")
	require.Empty(t, *f.faults, "the tripped brake withholds the slash")
	batch := f.batch(t, 0)
	require.Equal(t, batchOpen, batch.Status, "and the forfeiture of the batch with it")
	require.Equal(t, f.taskID, batch.ForfeitingTaskId)
	require.Equal(t, shared.NewAmount(100), batch.WorkerDue)
	return settledAt / latePeriod
}

// While the per-model brake withholds a rejection's slash, the Worker's batch
// is neither forfeited nor paid. A release that applies the withheld effects
// forfeits it.
func TestBrakeWithholdsTheBatchForfeitureUntilReleaseApplies(t *testing.T) {
	f := newBrakedBatchFixture(t)
	period := f.rejectUnderBrake(t)
	require.Equal(t, uint64(1), f.closes(t, latePeriod))
	require.Equal(t, batchClosing, f.batch(t, 0).Status, "a marked batch is not queued")
	require.Zero(t, f.pays(t, latePeriod))

	core := mustCore(t, f.anchorOnlyFixture)
	resolved, err := f.keeper.resolveAnchorBrakeEffects(f.at(int64(latePeriod+1)), core.ModelId, period, true, latePeriod+1)
	require.NoError(t, err)
	require.Equal(t, uint32(1), resolved)
	require.Len(t, *f.faults, 1)
	require.Equal(t, hubtypes.FaultTypeAnchorReject, (*f.faults)[0].FaultType)
	batch := f.batch(t, 0)
	require.Equal(t, batchForfeitedCheat, batch.Status)
	require.Equal(t, shared.NewAmount(100), batch.ForfeitedAmount)
	require.Equal(t, int64(100), f.transfers(shared.TaskChallengeEffectModuleName, hubtypes.TreasuryModuleName))
	require.Zero(t, f.credited(f.worker))
}

// A release that discards the withheld effects clears the mark: the batch is
// queued and paid, and nothing is slashed.
func TestBrakeDiscardLetsTheBatchBePaid(t *testing.T) {
	f := newBrakedBatchFixture(t)
	period := f.rejectUnderBrake(t)
	f.closes(t, latePeriod)
	core := mustCore(t, f.anchorOnlyFixture)
	_, err := f.keeper.resolveAnchorBrakeEffects(f.at(int64(latePeriod+1)), core.ModelId, period, false, latePeriod+1)
	require.NoError(t, err)
	batch := f.batch(t, 0)
	require.Equal(t, batchQueued, batch.Status)
	require.Empty(t, batch.ForfeitingTaskId)
	require.Equal(t, uint64(1), f.pays(t, latePeriod+1))
	require.Equal(t, uint64(100), f.credited(f.worker))
	require.Empty(t, *f.faults)
}

// What keeps a Worker's unbonded stake from being withdrawn: an audit that
// timed out and is not final, and a batch that is neither paid nor forfeited.
func TestOpenObligationsCoverLateAuditsAndUnpaidBatches(t *testing.T) {
	f := newBatchFixture(t, 4_000, 6)
	open, err := f.keeper.WorkerHasOpenObligations(f.ctx, f.worker)
	require.NoError(t, err)
	require.True(t, open, "its batch of the period is open")
	f.passAndSettle(t)
	f.closes(t, latePeriod)
	open, err = f.keeper.WorkerHasOpenObligations(f.ctx, f.worker)
	require.NoError(t, err)
	require.True(t, open, "a queued batch is not paid yet")
	f.pays(t, latePeriod)
	open, err = f.keeper.WorkerHasOpenObligations(f.ctx, f.worker)
	require.NoError(t, err)
	require.False(t, open)

	g := newAnchorLateFixture(t, 4_000, 6)
	g.timeOutTheProof(t)
	open, err = g.keeper.WorkerHasOpenObligations(g.ctx, g.worker)
	require.NoError(t, err)
	require.True(t, open, "an audit waits for its late submission")
}

// A panel assignment carries the registration hash of the task's Profile.
func TestPanelAssignmentCarriesTheRegistrationHash(t *testing.T) {
	f := newAnchorPanelFixture(t, 4_000, 6)
	round := f.assign(t)
	list, err := (queryServer{k: f.keeper}).AnchorPanelAssignments(f.ctx, &types.QueryAnchorPanelAssignmentsRequest{VerifierOperatorAddress: round.Members[0]})
	require.NoError(t, err)
	require.Len(t, list.Assignments, 1)
	core := mustCore(t, f.anchorOnlyFixture)
	require.Equal(t, f.keeper.AnchorRegistrationHash(f.ctx, core.ModelId, core.ProfileVersion), list.Assignments[0].RegistrationHash)
}
