package keeper

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TrueOpen/node/x/task/types"
)

// A task accepted before the upgrade is bound to the params version of that
// time. After the body is committed again as a new version it still reads the
// same body, so an audit in flight keeps its mode and every other parameter.
func TestRecommitKeepsTheBodyOfTasksInFlight(t *testing.T) {
	f := initInternalFixture(t)
	updateParams(t, f, func(p *types.TaskParamsV1) { p.Session.SessionIdleTtlBlocks += 7 })
	body, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	meta, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	inFlight := types.NewTaskKey(bytes.Repeat([]byte{0x11}, types.Hash32Len))
	require.NoError(t, f.keeper.TaskCore.Set(f.ctx, inFlight, types.TaskCoreState{TaskId: inFlight}))
	require.NoError(t, f.keeper.PinTaskParams(f.ctx, inFlight))
	require.NoError(t, f.keeper.EnsureParamsVersionInvariant(f.ctx))

	// A commitment that already matches is left alone.
	require.NoError(t, f.keeper.RecommitParams(f.ctx))
	same, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.Equal(t, meta, same)

	// The previous binary's commitment of the same body.
	stale := meta
	stale.ParamsHash = bytes.Repeat([]byte{0x5a}, types.Hash32Len)
	require.NoError(t, f.keeper.ParamsMeta.Set(f.ctx, stale))
	require.ErrorContains(t, f.keeper.EnsureParamsVersionInvariant(f.ctx), "hash disagrees with its body")
	require.NoError(t, f.keeper.RecommitParams(f.ctx))

	after, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.Equal(t, meta.ParamsVersion+1, after.ParamsVersion)
	current, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, body, current, "the body is not changed")
	forTask, err := f.keeper.ParamsForTask(f.ctx, inFlight)
	require.NoError(t, err)
	require.Equal(t, body, forTask)
	require.Zero(t, forTask.Verification.AnchorAuditMode)
	require.NoError(t, f.keeper.EnsureParamsVersionInvariant(f.ctx))
	created := types.NewTaskKey(bytes.Repeat([]byte{0x12}, types.Hash32Len))
	require.NoError(t, f.keeper.TaskCore.Set(f.ctx, created, types.TaskCoreState{TaskId: created}))
	require.NoError(t, f.keeper.PinTaskParams(f.ctx, created))
	require.NoError(t, f.keeper.EnsureParamsVersionInvariant(f.ctx))
	forNew, err := f.keeper.ParamsForTask(f.ctx, created)
	require.NoError(t, err)
	require.Equal(t, body, forNew)
}
