package keeper

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

// Regression tests for the findings of the release-candidate audit of two-step
// sampling (CH-1 .. CH-8). Each test names its finding.

// fillSamplingQueue places n other two-step audits in the EndBlock queue, due
// at height, so that a block's byte budget is used up before the fixture's own
// audit is reached.
func (f *twoStepFixture) fillSamplingQueue(t *testing.T, n int, due, listDeadline uint64) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := make([]byte, 32)
		id[0], id[1] = 0xEE, byte(i)
		key := types.NewTaskKey(id)
		require.NoError(t, f.keeper.AnchorAudit.Set(f.ctx, key, types.AnchorAuditState{
			TaskId: id, Sampled: true, Selected: true, AuditBasis: "x", SamplingMode: anchorSamplingModeTwoStep,
			ListDeadlineHeight: listDeadline, VerdictDeadlineHeight: 1_000_000,
		}))
		require.NoError(t, f.keeper.setAnchorSamplingDue(f.ctx, key, due))
	}
}

// CH-1. A Worker records its list in the last block of the list window, and
// the block of the draw is full: 64 other audits use up the EndBlock byte
// budget, so the draw is visited one block late. Before the fix this ended the
// audit NO_VERDICT. Now the draw is made in the next block and the opening,
// proof and verdict windows keep their full length. (The audit's reproduction
// TestAuditF_ZeroSlackListPlusFullEndBlockVoidsTheAudit, with the outcome it
// must have now.)
func TestAuditF_ZeroSlackListPlusFullEndBlockStillDraws(t *testing.T) {
	f := newTwoStepFixture(t, 4_000)
	state := f.selected(t)
	recorded := f.postList(t, state.ListDeadlineHeight) // the Worker's choice of height
	last := recorded.PositionBeaconLastHeight
	f.fillSamplingQueue(t, 64, last-1, last-2)

	result, _, err := f.keeper.runEndBlockDeadlineSweepsWithBudget(f.at(int64(last)), last, 1024, maxDeadlineSweepBytesPerBlockV1, 10_000)
	require.NoError(t, err)
	require.Equal(t, uint64(64), result.AnchorSampling, "the byte budget of the block is spent on the 64 other audits")
	require.Empty(t, f.audit(t).Positions, "the Worker's draw did not fit into the block")
	require.Zero(t, f.audit(t).ResultHeight)

	_, _, err = f.keeper.runEndBlockDeadlineSweepsWithBudget(f.at(int64(last+1)), last+1, 1024, maxDeadlineSweepBytesPerBlockV1, 10_000)
	require.NoError(t, err)
	drawn := f.audit(t)
	require.Zero(t, drawn.ResultHeight, "one block of EndBlock delay must not end the audit")
	require.NotEmpty(t, drawn.Positions, "the draw is made one block late")
	require.Equal(t, last+1, drawn.PositionsHeight)
	require.Equal(t, recorded.PositionBeaconFirstHeight, drawn.PositionBeaconFirstHeight, "from the heights the list fixed")
	require.Equal(t, last, drawn.PositionBeaconLastHeight)

	open, proof, verdict := f.windows()
	require.Equal(t, drawn.PositionsHeight+open, drawn.OpeningDeadlineHeight, "full opening window, counted from the draw")
	require.Equal(t, drawn.OpeningDeadlineHeight+proof, drawn.ProofDeadlineHeight, "full proof window")
	require.LessOrEqual(t, drawn.ProofDeadlineHeight+verdict, drawn.VerdictDeadlineHeight, "full verdict window")
	require.Equal(t, state.VerdictDeadlineHeight, drawn.VerdictDeadlineHeight, "the verdict deadline fixed at selection is not moved")
	require.Equal(t, state.ListDeadlineHeight, drawn.ListDeadlineHeight)

	// The audit then runs like any other: the honest Worker opens, proves, and is paid.
	f.openAndProveDrawn(t, drawn)
	require.Empty(t, *f.faults)
}

// CH-1. The grace is the same whatever height the Worker chose for its list:
// the last height at which the chain still draws is list deadline + m + grace.
// At that height the draw is made with full windows; one block later it is
// not, and the audit ends NO_VERDICT (paid), never with shortened windows.
func TestAuditF_DrawGraceDoesNotDependOnTheListHeight(t *testing.T) {
	open, proof, verdict := uint64(0), uint64(0), uint64(0)
	for _, early := range []bool{true, false} {
		f := newTwoStepFixture(t, 4_000)
		state := f.selected(t)
		open, proof, verdict = f.windows()
		bound := state.ListDeadlineHeight + f.m + types.AnchorPositionDrawGraceBlocks
		require.Equal(t, bound+open+proof+verdict, state.VerdictDeadlineHeight)
		listAt := state.ListDeadlineHeight
		if early {
			listAt = state.SampleHeight + 1
		}
		f.postList(t, listAt)
		require.Equal(t, uint64(1), f.sweep(t, bound))
		drawn := f.audit(t)
		require.NotEmpty(t, drawn.Positions, "a visit at the bound still draws (early list: %v)", early)
		require.Equal(t, bound+open, drawn.OpeningDeadlineHeight)
		require.Equal(t, bound+open+proof, drawn.ProofDeadlineHeight)
		require.Equal(t, drawn.ProofDeadlineHeight+verdict, drawn.VerdictDeadlineHeight)
	}

	f := newTwoStepFixture(t, 4_000)
	state := f.selected(t)
	f.postList(t, state.ListDeadlineHeight)
	late := state.ListDeadlineHeight + f.m + types.AnchorPositionDrawGraceBlocks + 1
	require.Equal(t, uint64(1), f.sweep(t, late))
	final := f.audit(t)
	require.Empty(t, final.Positions)
	require.Equal(t, anchorNoVerdict, final.TaskVerdict)
	settlement, err := f.settleNow(t, final.ResultHeight)
	require.NoError(t, err)
	f.requirePaidInFull(t, settlement)
	require.Empty(t, *f.faults)
}

// CH-1. A list in the last block of the window and a contributing beacon that
// is not a proposer-VRF value: the window restarts inside the grace and the
// draw is made. Before the fix no further window fitted.
func TestAuditF_ZeroSlackListSurvivesARestartedBeaconWindow(t *testing.T) {
	f := newTwoStepFixture(t, 4_000)
	state := f.selected(t)
	recorded := f.postList(t, state.ListDeadlineHeight)
	f.hub.placeholder[int64(recorded.PositionBeaconFirstHeight)] = true
	require.Equal(t, uint64(1), f.sweep(t, recorded.PositionBeaconLastHeight))
	restarted := f.audit(t)
	require.Zero(t, restarted.ResultHeight)
	require.Empty(t, restarted.Positions)
	require.Equal(t, recorded.PositionBeaconLastHeight+1, restarted.PositionBeaconFirstHeight)
	require.Equal(t, uint64(1), f.sweep(t, restarted.PositionBeaconLastHeight))
	drawn := f.audit(t)
	require.NotEmpty(t, drawn.Positions)
	open, proof, verdict := f.windows()
	require.Equal(t, drawn.PositionsHeight+open, drawn.OpeningDeadlineHeight)
	require.LessOrEqual(t, drawn.OpeningDeadlineHeight+proof+verdict, drawn.VerdictDeadlineHeight)
}

// CH-1. The list window itself is not touched by the grace: the block of the
// list deadline is in time, the next one is not, exactly as before.
func TestAuditF_DrawGraceDoesNotMoveTheListDeadline(t *testing.T) {
	f := newTwoStepFixture(t, 0)
	state := f.selected(t)
	require.Equal(t, f.window.SelectionRandomnessHeight+f.listWindow, state.ListDeadlineHeight)
	_, err := f.server.SubmitAnchorCommitmentList(f.at(int64(state.ListDeadlineHeight+1)), f.listMsg(0x1a))
	require.ErrorContains(t, err, "deadline")
	f.postList(t, state.ListDeadlineHeight)
}

// CH-1. An audit selected before the grace existed carries the verdict
// deadline written then. It is never moved: such an audit keeps the bound it
// was given (and its Worker the windows it was promised).
func TestAuditF_AuditsSelectedBeforeTheGraceKeepTheirDeadline(t *testing.T) {
	f := newTwoStepFixture(t, 4_000)
	state := f.selected(t)
	open, proof, verdict := f.windows()
	old := state
	old.VerdictDeadlineHeight = state.ListDeadlineHeight + f.m + open + proof + verdict
	require.NoError(t, f.keeper.AnchorAudit.Set(f.ctx, f.taskKey, old))
	recorded := f.postList(t, state.SampleHeight+1)
	require.Equal(t, uint64(1), f.sweep(t, recorded.PositionBeaconLastHeight))
	drawn := f.audit(t)
	require.NotEmpty(t, drawn.Positions)
	require.Equal(t, old.VerdictDeadlineHeight, drawn.VerdictDeadlineHeight)
	require.LessOrEqual(t, drawn.ProofDeadlineHeight+verdict, drawn.VerdictDeadlineHeight)
}

// CH-3. A two-step task whose frozen parameters carry no window runs on the
// Recommended values (1,800 + 8 blocks). The retention bound that parameter
// validation enforces did not count them (validated 8,400 against a real audit
// life of 10,208 in the audit's reproduction
// TestAuditF_FallbackWindowsAreNotInTheRetentionBound). The bound is now the
// real life of the audit, fallback windows and draw grace included.
func TestAuditF_RetentionBoundCoversTheFallbackTwoStepAudit(t *testing.T) {
	p := types.DefaultTaskParams().WithAnchorDefaults()
	require.False(t, p.Verification.AnchorTwoStepConfigured())
	listWindow, m := p.Verification.AnchorTwoStepWindows()
	require.Equal(t, types.RecommendedAnchorListWindowBlocks, listWindow)
	require.Equal(t, uint64(types.RecommendedAnchorPositionBeaconBlocks), m)
	v := p.Verification
	life := listWindow + m + types.AnchorPositionDrawGraceBlocks + v.AnchorOpenWindowBlocks + v.AnchorProofWindowBlocks + v.AnchorVerdictWindowBlocks
	require.Equal(t, life, v.AnchorTotalWindowBlocks(), "the validated bound is the real life of a fallback two-step audit")

	p.Cleanup.TaskTerminalSummaryRetentionBlocks = life
	require.ErrorContains(t, p.Validate(), "task_terminal_summary_retention_blocks", "a retention the audit can outlive is refused")
	p.Cleanup.TaskTerminalSummaryRetentionBlocks = life + 1
	require.NoError(t, p.Validate())

	// The value the audit's reproduction showed to be accepted: one block more
	// than the three one-step windows.
	p.Cleanup.TaskTerminalSummaryRetentionBlocks = v.AnchorOpenWindowBlocks + v.AnchorProofWindowBlocks + v.AnchorVerdictWindowBlocks + 1
	require.Error(t, p.Validate())
	// A body recorded under the earlier rule stays valid as history and keeps
	// its commitment; it is only refused as a body to put in force.
	require.NoError(t, types.ValidateHistoricalTaskParams(p))
	_, err := types.TaskParamsHashV1("c", 1, p)
	require.NoError(t, err)
	// The repair an upgrade applies raises the retention above the real life.
	repaired, notes := types.RepairAnchorParams(p)
	require.NotEmpty(t, notes)
	require.Greater(t, repaired.Cleanup.TaskTerminalSummaryRetentionBlocks, repaired.Verification.AnchorTotalWindowBlocks())
	require.NoError(t, repaired.Validate())
}

// CH-3. The bound equals what the keeper really writes: the span from the
// block that records the selection to the verdict deadline, for a task on the
// fallback windows and for a task on configured ones.
func TestAuditF_RetentionBoundEqualsTheWrittenAuditLife(t *testing.T) {
	for _, configured := range []bool{false, true} {
		f := newTwoStepFixture(t, 0)
		if !configured {
			f.params.Verification.AnchorListWindowBlocks, f.params.Verification.AnchorPositionBeaconBlocks = 0, 0
			require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
		}
		state := f.selected(t)
		selection := f.window.SelectionRandomnessHeight
		require.Equal(t, f.params.Verification.AnchorTotalWindowBlocks(), state.VerdictDeadlineHeight-selection, "configured: %v", configured)
		require.NoError(t, f.params.Validate())
		require.Less(t, state.VerdictDeadlineHeight-selection, f.params.Cleanup.TaskTerminalSummaryRetentionBlocks)
	}
}

// CH-5. A wrong first list location could not be corrected: a repetition with
// another URL was a NOOP and the stored URL stayed (the audit's reproduction
// TestAuditF_ListLocationCannotBeCorrected). The selected Worker can now
// correct the location with the same message, the same hash and the same
// size. Nothing else of the audit moves.
func TestAuditF_ListLocationCanBeCorrected(t *testing.T) {
	f := newTwoStepFixture(t, 0)
	state := f.selected(t)
	recorded := f.postList(t, state.SampleHeight+1)
	require.Equal(t, "https://da.example/list", recorded.CommitmentListUri)

	msg := f.listMsg(0x1a)
	msg.ListUri = "https://da.example/corrected"
	ctx := sdk.UnwrapSDKContext(f.at(int64(state.SampleHeight + 2))).WithEventManager(sdk.NewEventManager())
	resp, err := f.server.SubmitAnchorCommitmentList(ctx, msg)
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, resp.Status)
	require.Equal(t, recorded.PositionBeaconFirstHeight, resp.PositionBeaconFirstHeight)
	require.Equal(t, recorded.PositionBeaconLastHeight, resp.PositionBeaconLastHeight)
	want := recorded
	want.CommitmentListUri = "https://da.example/corrected"
	require.Equal(t, want, f.audit(t), "only the location changes: not the hash, the size, the list height or the beacon window")
	due, queued := f.dueHeight(t)
	require.True(t, queued)
	require.Equal(t, recorded.PositionBeaconLastHeight, due, "the draw stays where the first message put it")
	var events int
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "task.v1.EventAnchorCommitmentListRecorded" {
			events++
		}
	}
	require.Equal(t, 1, events, "a correction that changes the row is announced")

	// The corrected location again is a NOOP.
	resp, err = f.server.SubmitAnchorCommitmentList(f.at(int64(state.SampleHeight+3)), msg)
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_NOOP, resp.Status)

	// The positions do not depend on the location.
	other := newTwoStepFixture(t, 0)
	otherState := other.selected(t)
	plain := other.drawn(t, otherState.SampleHeight+1)
	require.Equal(t, uint64(1), f.sweep(t, recorded.PositionBeaconLastHeight))
	require.Equal(t, plain.Positions, f.audit(t).Positions)
	require.Equal(t, plain.PositionBeacon, f.audit(t).PositionBeacon)
}

// CH-5. The correction is open as long as the audit is: after the draw and
// after the list deadline too. A different hash or size stays refused exactly
// as before, at every stage; so do another signer and a location outside the
// frozen data-availability prefix. After the verdict is final the row is a
// record: the message is a NOOP and the location stays.
func TestAuditF_ListLocationCorrectionRules(t *testing.T) {
	f := newTwoStepFixture(t, 0)
	state := f.selected(t)
	*f.hub.endpoint = "https://da.example/"
	state.DaUriPrefix = "https://da.example/"
	require.NoError(t, f.keeper.AnchorAudit.Set(f.ctx, f.taskKey, state))
	recorded := f.postList(t, state.SampleHeight+1)

	correct := func(height uint64, uri string, mutate func(*types.MsgSubmitAnchorCommitmentList)) (*types.MsgSubmitAnchorCommitmentListResponse, error) {
		msg := f.listMsg(0x1a)
		msg.ListUri = uri
		if mutate != nil {
			mutate(msg)
		}
		return f.server.SubmitAnchorCommitmentList(f.at(int64(height)), msg)
	}
	refused := func(height uint64) {
		t.Helper()
		before := f.audit(t)
		_, err := correct(height, "https://da.example/x", func(m *types.MsgSubmitAnchorCommitmentList) { m.ListHash = bytes32(0x2b) })
		require.ErrorContains(t, err, "first recorded list is final", "a different hash")
		_, err = correct(height, "https://da.example/x", func(m *types.MsgSubmitAnchorCommitmentList) { m.ListSizeBytes = 32 * (f.rows + 1) })
		require.ErrorContains(t, err, "first recorded list is final", "a different size")
		_, err = correct(height, "https://da.example/x", func(m *types.MsgSubmitAnchorCommitmentList) { m.SubmitterAddress = f.operators[1] })
		require.ErrorContains(t, err, "not the winner Worker", "another signer")
		_, err = correct(height, "https://elsewhere.example/list", nil)
		require.ErrorContains(t, err, "data-availability endpoint", "outside the frozen prefix")
		_, err = correct(height, "ftp://da.example/list", nil)
		require.ErrorContains(t, err, "http or https")
		require.Equal(t, before, f.audit(t), "a refused message changes nothing")
	}
	applied := func(height uint64, uri string) {
		t.Helper()
		before := f.audit(t)
		resp, err := correct(height, uri, nil)
		require.NoError(t, err)
		require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, resp.Status)
		before.CommitmentListUri = uri
		require.Equal(t, before, f.audit(t))
	}

	// Before the draw.
	refused(state.SampleHeight + 2)
	applied(state.SampleHeight+2, "https://da.example/a")
	// After the draw.
	require.Equal(t, uint64(1), f.sweep(t, recorded.PositionBeaconLastHeight))
	drawn := f.audit(t)
	require.NotEmpty(t, drawn.Positions)
	refused(drawn.PositionsHeight + 1)
	applied(drawn.PositionsHeight+1, "https://da.example/b")
	require.Equal(t, drawn.Positions, f.audit(t).Positions)
	// After the list deadline (the hash was recorded in time).
	refused(drawn.ListDeadlineHeight + 5)
	applied(drawn.ListDeadlineHeight+5, "https://da.example/c")
	// After the verdict is final: a NOOP, the location stays.
	f.openAndProveDrawn(t, f.audit(t))
	_, err := f.vote(0, f.valAcct[0], anchorReject, drawn.PositionsHeight+3)
	require.NoError(t, err)
	_, err = f.vote(1, f.valAcct[1], anchorReject, drawn.PositionsHeight+3)
	require.NoError(t, err)
	final := f.audit(t)
	require.NotZero(t, final.ResultHeight)
	resp, err := correct(drawn.PositionsHeight+4, "https://da.example/d", nil)
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_NOOP, resp.Status)
	require.Equal(t, final, f.audit(t))
	_, err = correct(drawn.PositionsHeight+4, "https://da.example/d", func(m *types.MsgSubmitAnchorCommitmentList) { m.ListHash = bytes32(0x2b) })
	require.ErrorContains(t, err, "first recorded list is final")
}

// CH-5. A correction cannot be used to record a list late: without a recorded
// list the message is a first list and is refused after the deadline.
func TestAuditF_ListLocationCorrectionIsNotALateList(t *testing.T) {
	f := newTwoStepFixture(t, 0)
	state := f.selected(t)
	msg := f.listMsg(0x1a)
	msg.ListUri = "https://da.example/corrected"
	_, err := f.server.SubmitAnchorCommitmentList(f.at(int64(state.ListDeadlineHeight+1)), msg)
	require.ErrorContains(t, err, "deadline")
	require.Empty(t, f.audit(t).CommitmentListHash)
}

// CH-7. On a round that did not pass, a missing opening (one step) is applied
// only by a settlement at or after the verdict deadline: before it the audit
// imposes nothing, because the Worker is not paid either way. A missing list
// (two steps) was applied from the block after the list deadline instead, long
// before. The two are now read at the same moments, whether or not the EndBlock
// queue has already recorded the missing list.
func TestAuditF_MissingListOnAFailedRoundIsTimedLikeAMissingOpening(t *testing.T) {
	pass, fail := types.TaskVerdict_TASK_VERDICT_PASS, types.TaskVerdict_TASK_VERDICT_FAIL
	rejectTimeout := func(view *types.AnchorAuditState) {
		t.Helper()
		require.NotNil(t, view)
		require.Equal(t, anchorReject, view.TaskVerdict)
		require.Equal(t, types.AnchorResultSourceV1_ANCHOR_RESULT_SOURCE_V1_TIMEOUT, view.ResultSource)
	}

	// The one-step reference: a selected audit, no opening.
	one := newAnchorOnlyFixture(t, 4_000, true)
	one.submitReceipt(t)
	oneState := one.sampleRound(t)
	require.NotEmpty(t, oneState.Positions)
	view, err := one.keeper.anchorSettlementView(one.ctx, one.taskKey, fail, oneState.VerdictDeadlineHeight-1)
	require.NoError(t, err)
	require.Nil(t, view, "one step, failed round, before the verdict deadline: nothing is imposed")
	view, err = one.keeper.anchorSettlementView(one.ctx, one.taskKey, fail, oneState.VerdictDeadlineHeight)
	require.NoError(t, err)
	rejectTimeout(view)

	for _, queueRan := range []bool{false, true} {
		f := newTwoStepFixture(t, 4_000)
		state := f.selected(t)
		after := state.ListDeadlineHeight + 1
		if queueRan {
			require.Equal(t, uint64(1), f.sweep(t, after))
			recorded := f.audit(t)
			require.Equal(t, anchorReject, recorded.TaskVerdict, "the row still records the missing list when it is established")
			require.Equal(t, after, recorded.ResultHeight)
		}
		for _, height := range []uint64{after, after + 10, state.VerdictDeadlineHeight - 1} {
			view, err := f.keeper.anchorSettlementView(f.ctx, f.taskKey, fail, height)
			require.NoError(t, err)
			require.Nil(t, view, "two steps, failed round, height %d before the verdict deadline (queue ran: %v)", height, queueRan)
		}
		view, err := f.keeper.anchorSettlementView(f.ctx, f.taskKey, fail, state.VerdictDeadlineHeight)
		require.NoError(t, err)
		rejectTimeout(view)
		require.Equal(t, anchorListMissingReason, view.ResultReason)

		// A PASS round is unchanged: the established omission decides the task
		// from the block after the list deadline, and never earlier.
		_, err = f.keeper.anchorSettlementView(f.ctx, f.taskKey, pass, state.ListDeadlineHeight)
		if queueRan {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "pending until")
		}
		view, err = f.keeper.anchorSettlementView(f.ctx, f.taskKey, pass, after)
		require.NoError(t, err)
		rejectTimeout(view)
	}
}

// CH-7. The alignment touches the missing list only. A verdict the validators
// reached, or a NO_VERDICT of the chain, is read as before on a failed round.
func TestAuditF_FailedRoundStillReadsOtherFinalVerdicts(t *testing.T) {
	fail := types.TaskVerdict_TASK_VERDICT_FAIL
	f := newTwoStepFixture(t, 4_000)
	state := f.selected(t)
	drawn := f.drawn(t, state.SampleHeight+1)
	f.openAndProveDrawn(t, drawn)
	_, err := f.vote(0, f.valAcct[0], anchorReject, drawn.PositionsHeight+3)
	require.NoError(t, err)
	_, err = f.vote(1, f.valAcct[1], anchorReject, drawn.PositionsHeight+3)
	require.NoError(t, err)
	require.NotZero(t, f.audit(t).ResultHeight)
	view, err := f.keeper.anchorSettlementView(f.ctx, f.taskKey, fail, drawn.PositionsHeight+4)
	require.NoError(t, err)
	require.NotNil(t, view)
	require.Equal(t, anchorReject, view.TaskVerdict)
	require.Equal(t, types.AnchorResultSourceV1_ANCHOR_RESULT_SOURCE_V1_VALIDATOR_VOTE, view.ResultSource)
}
