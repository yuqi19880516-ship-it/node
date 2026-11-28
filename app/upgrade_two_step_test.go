package app

import (
	"reflect"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/stretchr/testify/require"

	shared "github.com/TrueOpen/node/x/shared/types"
	taskmodule "github.com/TrueOpen/node/x/task/module"
	tasktypes "github.com/TrueOpen/node/x/task/types"
)

// taskParamsHashOfConsensusVersion4 is the params commitment as the anchor
// audit release (task consensus version 4) computed it: the verification
// section ended at field 17, anchor_timeout_slash_bps.
func taskParamsHashOfConsensusVersion4(t *testing.T, chainID string, version uint64, p tasktypes.TaskParamsV1) []byte {
	t.Helper()
	fields := tasktypes.CanonicalTaskParamsTypedFields(p)
	other := p
	other.Verification.AnchorTimeoutSlashBps++
	changed := tasktypes.CanonicalTaskParamsTypedFields(other)
	section := -1
	for i := range fields {
		if !reflect.DeepEqual(fields[i], changed[i]) {
			require.Equal(t, -1, section)
			section = i
		}
	}
	require.NotEqual(t, -1, section)
	v := p.Verification
	fields[section] = shared.NestedCanonicalFieldV1(shared.NewCanonicalFrameBuilderV1().Field(shared.RawCanonicalFieldsV1(
		shared.Uint32BE(v.WorkerInferTimeoutSlashBps),
		shared.Uint32BE(v.ResultRevealMissingSlashBps),
		shared.Uint32BE(v.BuilderFaultSlashBps),
		shared.Uint32BE(v.AnchorRejectSlashBps),
		shared.Uint64BE(v.AnchorVerdictWindowBlocks),
		shared.Uint32BE(v.AnchorAuditMinRateBps),
		shared.Uint32BE(v.AnchorAuditRiskMarginBps),
		shared.Uint64BE(v.AnchorOpenWindowBlocks),
		shared.Uint64BE(v.AnchorProofWindowBlocks),
		shared.Uint32BE(v.AnchorTimeoutSlashBps),
	)...).Build())
	hash, err := shared.NewCanonicalHashBuilderV1(shared.MustDomain(shared.DomainTaskParamsV1)).
		Raw([]byte(chainID), shared.Uint64BE(version)).
		Nested(shared.NewCanonicalFrameBuilderV1().Field(fields...).Build()).
		Sum()
	require.NoError(t, err)
	return hash
}

// The two-step sampling upgrade, run as a governance plan on a chain in the
// state the anchor audit release left: task module at consensus version 4 and
// a params commitment under that release's framing. Before the upgrade the
// parameter invariant fails; the plan runs the 4 -> 5 migration, after which
// the invariant holds, the module is at the binary's version, the chain keeps
// producing blocks and the application state exports.
func TestTwoStepSamplingUpgradeRecommitsTheTaskParams(t *testing.T) {
	app, valHash := bootUpgradeApp(t)
	advanceBlock(t, app, valHash)
	require.Equal(t, uint64(5), (taskmodule.AppModule{}).ConsensusVersion())

	ctx := upgradeCtx(app)
	body, err := app.TaskKeeper.Params.Get(ctx)
	require.NoError(t, err)
	stale := tasktypes.TaskParamsMetaState{
		ParamsVersion: 1, UpdatedHeight: 1,
		ParamsHash: taskParamsHashOfConsensusVersion4(t, SimAppChainID, 1, body),
	}
	require.NoError(t, app.TaskKeeper.ParamsMeta.Set(ctx, stale))
	versions, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	fromVM := module.VersionMap{}
	for name, version := range versions {
		fromVM[name] = version
	}
	fromVM[tasktypes.ModuleName] = 4
	require.NoError(t, app.UpgradeKeeper.SetModuleVersionMap(ctx, fromVM))
	require.ErrorContains(t, app.TaskKeeper.EnsureParamsVersionInvariant(ctx), "metadata hash disagrees",
		"the state the previous version left fails the invariant under this binary")

	upgradeHeight := app.LastBlockHeight() + 1
	scheduleUpgrade(t, app, TwoStepSamplingUpgradeName, upgradeHeight)
	require.Equal(t, upgradeHeight, advanceBlock(t, app, valHash))

	ctx = upgradeCtx(app)
	done, err := app.UpgradeKeeper.GetDoneHeight(ctx, TwoStepSamplingUpgradeName)
	require.NoError(t, err)
	require.Equal(t, upgradeHeight, done)
	after, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), after[tasktypes.ModuleName])

	require.NoError(t, app.TaskKeeper.EnsureParamsVersionInvariant(ctx))
	got, err := app.TaskKeeper.Params.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, body, got, "a valid body is not changed by the upgrade")
	meta, err := app.TaskKeeper.GetTaskParamsMeta(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), meta.ParamsVersion)
	require.Equal(t, uint64(upgradeHeight), meta.UpdatedHeight)
	want, err := tasktypes.TaskParamsHashV1(SimAppChainID, 2, got)
	require.NoError(t, err)
	require.Equal(t, want, meta.ParamsHash)
	archived, err := app.TaskKeeper.ParamsHistory.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, body, archived.Params)

	// The chain keeps producing blocks and the state exports.
	advanceBlock(t, app, valHash)
	exported, err := app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, exported.AppState)
}

func TestPlannedUpgradesCarryTheTwoStepSamplingPlan(t *testing.T) {
	app, _ := bootUpgradeApp(t)
	names := map[string]bool{}
	for _, u := range app.plannedUpgrades() {
		names[u.name] = true
	}
	require.True(t, names[AnchorAuditUpgradeName])
	require.True(t, names[TwoStepSamplingUpgradeName])
	require.NoError(t, validateUpgrades(app.plannedUpgrades()))
	require.True(t, app.UpgradeKeeper.HasHandler(TwoStepSamplingUpgradeName))
}
