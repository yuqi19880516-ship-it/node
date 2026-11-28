package app

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	hubmodule "github.com/TrueOpen/node/x/hub/module"
	hubtypes "github.com/TrueOpen/node/x/hub/types"
	taskmodule "github.com/TrueOpen/node/x/task/module"
	tasktypes "github.com/TrueOpen/node/x/task/types"
)

// The Verifier-panel upgrade runs on a chain whose parameter commitments were
// recorded by the previous binary, which framed fewer fields: the same bodies
// no longer match them. The upgrade commits both bodies again as new params
// versions, changes no parameter value, and the chain goes on.
func TestVerifierPanelUpgradeRecommitsParameterBodies(t *testing.T) {
	app, valHash := bootUpgradeApp(t)
	advanceBlock(t, app, valHash)
	ctx := upgradeCtx(app)

	taskParams, err := app.TaskKeeper.Params.Get(ctx)
	require.NoError(t, err)
	hubParams, err := app.HubKeeper.Params.Get(ctx)
	require.NoError(t, err)

	// State as the previous binary left it: module versions one lower and
	// commitments that the current framing does not reproduce.
	stale := bytes.Repeat([]byte{0x5a}, 32)
	require.NoError(t, app.TaskKeeper.ParamsMeta.Set(ctx, tasktypes.TaskParamsMetaState{ParamsVersion: 1, ParamsHash: stale, UpdatedHeight: 1}))
	require.NoError(t, app.HubKeeper.ParamsMeta.Set(ctx, hubtypes.HubParamsMetaState{ParamsVersion: 1, ParamsHash: stale, UpdatedHeight: 1}))
	require.ErrorContains(t, app.TaskKeeper.EnsureParamsVersionInvariant(ctx), "hash disagrees with its body",
		"the old commitment does not match under the new framing")
	versions, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	versions["task"] = (taskmodule.AppModule{}).ConsensusVersion() - 1
	versions["hub"] = (hubmodule.AppModule{}).ConsensusVersion() - 1
	require.NoError(t, app.UpgradeKeeper.SetModuleVersionMap(ctx, versions))

	upgradeHeight := app.LastBlockHeight() + 1
	scheduleUpgrade(t, app, TwoStepSamplingUpgradeName, upgradeHeight)
	require.Equal(t, upgradeHeight, advanceBlock(t, app, valHash))

	ctx = upgradeCtx(app)
	done, err := app.UpgradeKeeper.GetDoneHeight(ctx, TwoStepSamplingUpgradeName)
	require.NoError(t, err)
	require.Equal(t, upgradeHeight, done)
	versions, err = app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	require.Equal(t, (taskmodule.AppModule{}).ConsensusVersion(), versions["task"])
	require.Equal(t, (hubmodule.AppModule{}).ConsensusVersion(), versions["hub"])

	taskAfter, err := app.TaskKeeper.Params.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, taskParams, taskAfter, "no task parameter value changes")
	require.Zero(t, taskAfter.Verification.AnchorAuditMode, "the panel audit stays off until governance turns it on")
	taskMeta, err := app.TaskKeeper.GetTaskParamsMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), taskMeta.ParamsVersion)
	wantTask, err := tasktypes.TaskParamsHashV1(SimAppChainID, 2, taskAfter)
	require.NoError(t, err)
	require.Equal(t, wantTask, taskMeta.ParamsHash)
	require.NoError(t, app.TaskKeeper.EnsureParamsVersionInvariant(ctx))
	archived, err := app.TaskKeeper.ParamsHistory.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, taskParams, archived.Params, "objects bound to the replaced version read the same body")

	hubAfter, err := app.HubKeeper.Params.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, hubParams, hubAfter, "no Hub parameter value changes")
	hubMeta, err := app.HubKeeper.GetHubParamsMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), hubMeta.ParamsVersion)
	wantHub, err := hubtypes.HubParamsHash(SimAppChainID, 2, hubAfter)
	require.NoError(t, err)
	require.Equal(t, wantHub, hubMeta.ParamsHash)

	// The chain keeps producing blocks, and a second run changes nothing.
	advanceBlock(t, app, valHash)
	ctx = upgradeCtx(app)
	require.NoError(t, app.TaskKeeper.RecommitTaskParams(ctx))
	require.NoError(t, app.HubKeeper.RecommitParams(ctx))
	taskMeta, err = app.TaskKeeper.GetTaskParamsMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), taskMeta.ParamsVersion)
	hubMeta, err = app.HubKeeper.GetHubParamsMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), hubMeta.ParamsVersion)
}
