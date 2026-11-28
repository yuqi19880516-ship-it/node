package keeper

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

// panelTwoStepHubStub is the panel Hub view on a chain that requires the
// proposer-VRF beacon: every height has a verified beacon unless a test
// withholds it.
type panelTwoStepHubStub struct {
	panelHubStub
}

func (s panelTwoStepHubStub) BeaconSentinelRequiredAtHeight(context.Context, uint64) (bool, error) {
	return true, nil
}

func (s panelTwoStepHubStub) GetBeaconForDomain(_ sdk.Context, _ string, height int64) (hubtypes.BeaconSnapshot, bool) {
	if s.state.noBeacon[height] {
		return hubtypes.BeaconSnapshot{}, false
	}
	return hubtypes.BeaconSnapshot{
		Height: height, Randomness: testBlockBeacon(height),
		SourceTag: hubtypes.BeaconSourceProposerVRFV1, ProofDigest: bytes32(0x5d),
	}, true
}

// newPanelTwoStepFixture stages a task of a two-step Profile under the panel
// audit mode with late submission on. The first beacon has selected it; no
// position exists yet.
func newPanelTwoStepFixture(t *testing.T, candidates int) *anchorPanelFixture {
	t.Helper()
	f := newAnchorPanelFixtureWithSchema(t, 4_000, candidates, shared.WorkerAnchorCommitmentSchemaVersionV2, func(params *types.TaskParamsV1) {
		lateParams(lateFreeCount)(params)
		params.Verification.AnchorListWindowBlocks = twoStepListWindow
		params.Verification.AnchorPositionBeaconBlocks = twoStepBeacons
	}, func(base *anchorOnlyFixture) {
		base.keeper.hubKeeper = panelTwoStepHubStub{panelHubStub: base.keeper.hubKeeper.(panelHubStub)}
		base.server = msgServer{k: base.keeper}
	})
	require.Equal(t, anchorSamplingModeTwoStep, f.audit.SamplingMode)
	require.Empty(t, f.audit.Positions)
	return f
}

func (f *anchorPanelFixture) samplingSweep(t *testing.T, height uint64) uint64 {
	t.Helper()
	visited, err := f.keeper.processAnchorSamplingDue(f.at(int64(height)), height, 10)
	require.NoError(t, err)
	return visited
}

// postList records the Worker's commitment list at height.
func (f *anchorPanelFixture) postList(t *testing.T, height uint64) types.AnchorAuditState {
	t.Helper()
	rows := anchorOnlyPrompt + anchorOnlyGenerated - 1
	_, err := f.server.SubmitAnchorCommitmentList(f.at(int64(height)), &types.MsgSubmitAnchorCommitmentList{
		TaskId: f.taskID, ListHash: bytes32(0x1a), ListSizeBytes: 32 * rows,
		ListUri: "https://da.example/list", SubmitterAddress: f.worker,
	})
	require.NoError(t, err)
	return f.reload(t)
}

// drawAndProve takes the audit from its recorded list to the recorded proof:
// the draw at the last contributing height, then the opening and the proof.
func (f *anchorPanelFixture) drawAndProve(t *testing.T) types.AnchorAuditState {
	t.Helper()
	require.Equal(t, uint64(1), f.samplingSweep(t, f.audit.PositionBeaconLastHeight))
	drawn := f.reload(t)
	require.NotEmpty(t, drawn.Positions)
	require.NoError(t, f.openAt(drawn.PositionsHeight+1))
	_, err := f.proveAt(drawn.PositionsHeight+2, "p")
	require.NoError(t, err)
	return f.reload(t)
}

// The verdict deadline of a two-step panel audit is fixed at selection and
// covers the list window, the beacon heights, the draw grace, the Worker's
// windows and the whole panel schedule with its late window.
func TestPanelTwoStepVerdictDeadlineCoversBothSchedules(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	v := f.params.Verification
	require.Equal(t, f.audit.SampleHeight+twoStepListWindow, f.audit.ListDeadlineHeight)
	span, ok := f.params.AnchorPanelRoundSpanBlocks()
	require.True(t, ok)
	panelWindow := v.AnchorVerdictWindowBlocks + 4*span + panelPublication + lateWindow
	require.Equal(t, panelWindow, f.params.AnchorEffectiveVerdictWindowBlocks())
	want := f.audit.ListDeadlineHeight + uint64(twoStepBeacons) + types.AnchorPositionDrawGraceBlocks +
		v.AnchorOpenWindowBlocks + v.AnchorProofWindowBlocks + panelWindow
	require.Equal(t, want, f.audit.VerdictDeadlineHeight)
	require.Equal(t, f.audit.VerdictDeadlineHeight+1, f.settlementDeadline(t))
	_, err := f.settleNow(t, f.audit.SampleHeight+1)
	require.ErrorContains(t, err, "pending until")
}

// Two of three panel members pass a two-step audit: list, draw, opening and
// proof, panel round at the proof, publication, final PASS, Worker paid.
func TestPanelTwoStepHonestWorkerPassesByTwoOfThree(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	selected := f.audit
	listed := f.postList(t, selected.SampleHeight+3)
	require.Empty(t, listed.PanelRounds)
	proved := f.drawAndProve(t)
	require.Equal(t, selected.VerdictDeadlineHeight, proved.VerdictDeadlineHeight, "the verdict deadline never moves")
	require.Equal(t, proved.PositionsHeight+f.params.Verification.AnchorOpenWindowBlocks, proved.OpeningDeadlineHeight)
	require.Len(t, proved.PanelRounds, 1, "the first panel round opens with the proof")
	require.Equal(t, uint32(2), proved.PanelRound)

	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, votePass, votePass)
	audit := f.reload(t)
	require.Equal(t, votePass, audit.ProvisionalVerdict)
	require.Less(t, audit.PublicationEndHeight, audit.VerdictDeadlineHeight)
	f.sweep(t, audit.PublicationEndHeight)
	settlement, err := f.settleNow(t, audit.PublicationEndHeight+1)
	require.NoError(t, err)
	f.requireAuditedPaid(t, settlement)
	f.requireNoPanelWork(t)
}

// A panel rejection of a two-step audit is the rejection fault.
func TestPanelTwoStepRejectSlashesTheWorker(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	f.postList(t, f.audit.SampleHeight+1)
	f.drawAndProve(t)
	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, voteReject, voteReject)
	audit := f.reload(t)
	f.sweep(t, audit.PublicationEndHeight)
	settlement, err := f.settleNow(t, audit.PublicationEndHeight+1)
	require.NoError(t, err)
	f.requireWorkerFaulted(t, settlement, hubtypes.FaultTypeAnchorReject, f.params.Verification.AnchorRejectSlashBps, 4_000)
}

// A missing commitment list has no late form. One block after the list
// deadline the audit ends as the existing timeout: the task fails with the
// timeout fault, the Worker is not paused, nothing waits for a late
// submission, and a list sent afterwards is refused. Settlement reaches the
// same outcome without the queue.
func TestPanelTwoStepMissingListIsTheTimeoutWithoutALateForm(t *testing.T) {
	for _, byQueue := range []bool{true, false} {
		f := newPanelTwoStepFixture(t, 6)
		deadline := f.audit.ListDeadlineHeight
		require.Zero(t, f.samplingSweep(t, deadline))
		if byQueue {
			require.Equal(t, uint64(1), f.samplingSweep(t, deadline+1))
			audit := f.reload(t)
			require.Equal(t, voteReject, audit.TaskVerdict)
			require.Equal(t, types.AnchorResultSourceV1_ANCHOR_RESULT_SOURCE_V1_TIMEOUT, audit.ResultSource)
			require.Equal(t, deadline+1, audit.FinalHeight)
			require.Zero(t, audit.LateState)
		}
		_, err := f.server.SubmitAnchorCommitmentList(f.at(int64(deadline+2)), &types.MsgSubmitAnchorCommitmentList{
			TaskId: f.taskID, ListHash: bytes32(0x1a), ListSizeBytes: 32 * (anchorOnlyPrompt + anchorOnlyGenerated - 1),
			ListUri: "https://da.example/list", SubmitterAddress: f.worker,
		})
		require.Error(t, err, "there is no late list")
		require.False(t, f.workerPaused())
		require.Empty(t, f.lateList(t))
		settlement, err := f.settleNow(t, deadline+2)
		require.NoError(t, err)
		f.requireWorkerFaulted(t, settlement, hubtypes.FaultTypeAnchorTimeout, f.params.Verification.AnchorTimeoutSlashBps, 4_000)
		audit := f.reload(t)
		require.NotZero(t, audit.FinalHeight)
		require.Empty(t, audit.PanelRounds)
		f.requireNoPanelWork(t)
	}
}

// A contributing beacon that is missing restarts the beacon window. The
// Worker's windows count from the later draw, the verdict deadline stays, and
// the panel audit runs as usual. Nothing of this is the Worker's fault.
func TestPanelTwoStepBadBeaconRestartsTheListWindowDraw(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	listed := f.postList(t, f.audit.SampleHeight+2)
	f.hubState.noBeacon[int64(listed.PositionBeaconFirstHeight+1)] = true
	require.Equal(t, uint64(1), f.samplingSweep(t, listed.PositionBeaconLastHeight))
	restarted := f.reload(t)
	require.Empty(t, restarted.Positions)
	require.Zero(t, restarted.ResultHeight)
	require.Equal(t, listed.PositionBeaconLastHeight+1, restarted.PositionBeaconFirstHeight)
	require.Equal(t, listed.VerdictDeadlineHeight, restarted.VerdictDeadlineHeight)
	has, err := f.keeper.AnchorPanelDueHeight.Has(f.ctx, f.taskKey)
	require.NoError(t, err)
	require.False(t, has, "no deadline of the Worker exists before the draw")

	proved := f.drawAndProve(t)
	require.Equal(t, restarted.PositionBeaconLastHeight, proved.PositionsHeight)
	require.Equal(t, proved.PositionsHeight+f.params.Verification.AnchorOpenWindowBlocks, proved.OpeningDeadlineHeight)
	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, votePass, votePass)
	audit := f.reload(t)
	f.sweep(t, audit.PublicationEndHeight)
	settlement, err := f.settleNow(t, audit.PublicationEndHeight+1)
	require.NoError(t, err)
	f.requireAuditedPaid(t, settlement)
}

// Silent panels on a two-step audit cost the Worker nothing: no pause, no
// fault, the audit ends without a verdict inside its verdict deadline and the
// Worker is paid.
func TestPanelTwoStepSilentVerifiersPayTheWorker(t *testing.T) {
	f := newPanelTwoStepFixture(t, 12)
	f.postList(t, f.audit.SampleHeight+1)
	f.drawAndProve(t)
	audit := f.silence(t)
	require.False(t, f.workerPaused())
	require.LessOrEqual(t, audit.LateDeadlineHeight, audit.VerdictDeadlineHeight, "the late window lies inside the verdict deadline")
	settlement, err := f.settleNow(t, audit.LateDeadlineHeight)
	require.NoError(t, err)
	f.requireAuditedPaid(t, settlement)
	require.Empty(t, *f.faults)
}

// After the draw the Worker's deadlines are watched like those of a one-step
// audit: a missed opening is a timeout with a late window, and the late
// opening and proof cure it.
func TestPanelTwoStepMissedOpeningHasTheLatePath(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	listed := f.postList(t, f.audit.SampleHeight+1)
	require.Equal(t, uint64(1), f.samplingSweep(t, listed.PositionBeaconLastHeight))
	drawn := f.reload(t)
	require.Equal(t, drawn.OpeningDeadlineHeight+1, f.dueHeight(t), "the draw starts the deadline watch")
	require.Equal(t, uint64(1), f.sweep(t, drawn.OpeningDeadlineHeight+1))
	audit := f.reload(t)
	require.Equal(t, lateStateAwaiting, audit.LateState)
	require.Equal(t, reasonOpening, audit.TimeoutReason)
	require.True(t, f.workerPaused())
	require.NoError(t, f.openAt(audit.TimeoutRecordedHeight+1))
	_, err := f.proveAt(audit.TimeoutRecordedHeight+2, "p")
	require.NoError(t, err)
	require.False(t, f.workerPaused())
	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, votePass, votePass)
	audit = f.reload(t)
	f.sweep(t, audit.PublicationEndHeight)
	settlement, err := f.settleNow(t, audit.PublicationEndHeight+1)
	require.NoError(t, err)
	f.requireAuditedPaid(t, settlement)
}

// An import in the middle of a two-step panel audit restores both queues: the
// wait for the list, and after the draw the watch of the Worker's deadlines.
func TestPanelTwoStepSurvivesExportAndImport(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	// Before the list: only the sampling queue holds the audit.
	require.NoError(t, f.keeper.clearAnchorSamplingDue(f.ctx, f.taskKey))
	require.NoError(t, f.keeper.rebuildAnchorPanelIndexes(f.ctx, f.taskKey, f.audit))
	require.NoError(t, f.keeper.rebuildAnchorSamplingDue(f.ctx, f.taskKey, f.audit))
	due, err := f.keeper.AnchorSamplingDueHeight.Get(f.ctx, f.taskKey)
	require.NoError(t, err)
	require.Equal(t, f.audit.ListDeadlineHeight+1, due)
	// The panel sweep's visit after the import finds nothing to watch yet.
	require.Equal(t, uint64(1), f.sweep(t, f.dueHeight(t)))
	require.Zero(t, f.reload(t).LateState)
	require.Empty(t, *f.faults)

	listed := f.postList(t, f.audit.SampleHeight+2)
	require.Equal(t, uint64(1), f.samplingSweep(t, listed.PositionBeaconLastHeight))
	drawn := f.reload(t)
	// After the draw: the panel queue is rebuilt from the audit row.
	require.NoError(t, f.keeper.clearAnchorPanelDue(f.ctx, f.taskKey))
	require.NoError(t, f.keeper.rebuildAnchorPanelIndexes(f.ctx, f.taskKey, drawn))
	require.NoError(t, f.keeper.rebuildAnchorSamplingDue(f.ctx, f.taskKey, drawn))
	require.Equal(t, uint64(1), f.sweep(t, f.dueHeight(t)))
	require.Equal(t, drawn.OpeningDeadlineHeight+1, f.dueHeight(t))
	require.NoError(t, f.openAt(drawn.PositionsHeight+1))
	_, err = f.proveAt(drawn.PositionsHeight+2, "p")
	require.NoError(t, err)
	round := f.assign(t)
	f.castVotes(t, round, round.AssignmentHeight+1, votePass, votePass)
	audit := f.reload(t)
	f.sweep(t, audit.PublicationEndHeight)
	settlement, err := f.settleNow(t, audit.PublicationEndHeight+1)
	require.NoError(t, err)
	f.requireAuditedPaid(t, settlement)
}

// The task retention must outlast the whole life of an audit, and with a
// Verifier panel that life includes the panel schedule. Under the validator
// vote the bound is what it was.
func TestRetentionBoundCountsThePanelSchedule(t *testing.T) {
	f := newPanelTwoStepFixture(t, 6)
	params := f.params
	life := params.AnchorAuditLifeBlocks()
	require.Greater(t, life, params.Verification.AnchorTotalWindowBlocks())
	params.Cleanup.TaskTerminalSummaryRetentionBlocks = life
	require.Error(t, params.Validate())
	repaired, notes := types.RepairAnchorParams(params)
	require.NotEmpty(t, notes)
	require.Greater(t, repaired.Cleanup.TaskTerminalSummaryRetentionBlocks, life)
	require.NoError(t, repaired.Validate())

	for _, mode := range []uint32{0, 1} {
		vote := f.params
		vote.Verification.AnchorAuditMode = mode
		require.Equal(t, vote.Verification.AnchorTotalWindowBlocks(), vote.AnchorAuditLifeBlocks())
		require.Equal(t, vote.Verification.AnchorVerdictWindowBlocks, vote.AnchorEffectiveVerdictWindowBlocks())
	}
}
