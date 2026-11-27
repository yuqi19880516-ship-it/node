package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

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
