package keeper_test

import (
	"reflect"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shared "github.com/TrueOpen/node/x/shared/types"
	tasktypes "github.com/TrueOpen/node/x/task/types"
)

// taskParamsHashBeforeVerifierFlow is the params commitment as the binary of
// consensus version 4 (the anchor audit release) computed it: the same framing
// as TaskParamsHashV1 except that the verification section ends at field 17
// (anchor_timeout_slash_bps). Fields 18..43 (Verifier-panel audit, late
// submission, batch settlement, two-step sampling) did not exist. It is what a
// chain that ran that binary has in its params metadata.
func taskParamsHashBeforeVerifierFlow(t *testing.T, chainID string, version uint64, p tasktypes.TaskParamsV1) []byte {
	t.Helper()
	fields := tasktypes.CanonicalTaskParamsTypedFields(p)
	// The verification section is the one field that changes with a
	// verification parameter.
	other := p
	other.Verification.AnchorTimeoutSlashBps++
	changed := tasktypes.CanonicalTaskParamsTypedFields(other)
	require.Equal(t, len(fields), len(changed))
	section := -1
	for i := range fields {
		if !reflect.DeepEqual(fields[i], changed[i]) {
			require.Equal(t, -1, section, "exactly one section holds the verification parameters")
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
	body := shared.NewCanonicalFrameBuilderV1().Field(fields...).Build()
	hash, err := shared.NewCanonicalHashBuilderV1(shared.MustDomain(shared.DomainTaskParamsV1)).
		Raw([]byte(chainID), shared.Uint64BE(version)).
		Nested(body).
		Sum()
	require.NoError(t, err)
	return hash
}

// importedLiveChain imports a genesis with a live Session and Task whose
// parameters carry metadata (version 1 and its commitment), like a chain that
// has been running, and returns the fixture and the genesis.
func importedLiveChain(t *testing.T, mutate func(*tasktypes.TaskParamsV1)) (*fixture, *tasktypes.GenesisState) {
	t.Helper()
	f := initFixtureWithHub(t, epochHubKeeper{})
	chainID := sdk.UnwrapSDKContext(f.ctx).ChainID()
	genesis := taskGenesisV1(t, chainID)
	require.NotEmpty(t, genesis.TaskCores)
	hash, err := tasktypes.TaskParamsHashV1(chainID, 1, genesis.Params)
	require.NoError(t, err)
	genesis.ParamsMeta = tasktypes.TaskParamsMetaState{ParamsVersion: 1, ParamsHash: hash, UpdatedHeight: 1}
	require.NoError(t, f.keeper.InitGenesis(f.ctx, *genesis))
	if mutate != nil {
		body, err := f.keeper.Params.Get(f.ctx)
		require.NoError(t, err)
		mutate(&body)
		require.NoError(t, f.keeper.Params.Set(f.ctx, body))
	}
	return f, genesis
}

// leaveStateAsConsensusVersion4 rewrites the params metadata to what the
// previous binary stored for the same body and version.
func leaveStateAsConsensusVersion4(t *testing.T, f *fixture) tasktypes.TaskParamsMetaState {
	t.Helper()
	chainID := sdk.UnwrapSDKContext(f.ctx).ChainID()
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	meta, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.NotZero(t, meta.ParamsVersion)
	current, err := tasktypes.TaskParamsHashV1(chainID, meta.ParamsVersion, params)
	require.NoError(t, err)
	meta.ParamsHash = taskParamsHashBeforeVerifierFlow(t, chainID, meta.ParamsVersion, params)
	require.NotEqual(t, current, meta.ParamsHash, "the framing of the body changed, so its commitment did")
	require.NoError(t, f.keeper.ParamsMeta.Set(f.ctx, meta))
	return meta
}

// CH-2. A chain that ran the anchor audit release holds a params commitment
// under the framing of that release. Under the new binary the parameter
// invariant fails on it and genesis export is refused. The 4 -> 5 migration
// writes the body as a new params version with its commitment: the invariant
// holds, the export works and imports again, and the live Task keeps the body
// it was created under.
func TestRecommitMigrationRepairsTheStoredParamsCommitment(t *testing.T) {
	f, genesis := importedLiveChain(t, nil)
	chainID := sdk.UnwrapSDKContext(f.ctx).ChainID()
	body, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	old := leaveStateAsConsensusVersion4(t, f)

	// The defect, as the previous version left the state.
	require.ErrorContains(t, f.keeper.EnsureParamsVersionInvariant(f.ctx), "metadata hash disagrees")
	_, err = f.keeper.ExportGenesis(f.ctx)
	require.Error(t, err, "genesis export is refused while the commitment is stale")

	require.NoError(t, f.keeper.RecommitTaskParams(f.ctx))

	require.NoError(t, f.keeper.EnsureParamsVersionInvariant(f.ctx))
	got, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, body, got, "a valid body is not changed")
	meta, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.Equal(t, old.ParamsVersion+1, meta.ParamsVersion, "a new version, so that no version has two commitments")
	want, err := tasktypes.TaskParamsHashV1(chainID, meta.ParamsVersion, got)
	require.NoError(t, err)
	require.Equal(t, want, meta.ParamsHash)
	archived, err := f.keeper.ParamsHistory.Get(f.ctx, old.ParamsVersion)
	require.NoError(t, err)
	require.Equal(t, body, archived.Params, "the replaced version keeps its body")
	bound, err := f.keeper.ParamsForTask(f.ctx, tasktypes.NewTaskKey(genesis.TaskCores[0].TaskId))
	require.NoError(t, err)
	require.Equal(t, body, bound, "a live Task keeps the parameters it was accepted under")

	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())
	g := initFixtureWithHub(t, epochHubKeeper{})
	// The export is imported by a chain that starts after the migration height.
	g.ctx = sdk.WrapSDKContext(sdk.UnwrapSDKContext(g.ctx).WithBlockHeight(sdk.UnwrapSDKContext(f.ctx).BlockHeight() + 1))
	require.NoError(t, g.keeper.InitGenesis(g.ctx, *exported), "the export of the migrated state imports")
	require.NoError(t, g.keeper.EnsureParamsVersionInvariant(g.ctx))

	// Running it again changes nothing.
	require.NoError(t, f.keeper.RecommitTaskParams(f.ctx))
	again, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.Equal(t, meta, again)
}

// A chain whose commitment already matches (it started on this framing) is
// left alone: no version is written.
func TestRecommitMigrationLeavesAMatchingCommitmentAlone(t *testing.T) {
	f, _ := importedLiveChain(t, nil)
	before, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.NoError(t, f.keeper.RecommitTaskParams(f.ctx))
	after, err := f.keeper.GetTaskParamsMeta(f.ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = f.keeper.ParamsHistory.Get(f.ctx, before.ParamsVersion)
	require.Error(t, err, "nothing was archived")
}

// CH-2 with CH-3. A live body whose task retention is above the three one-step
// windows but not above the full life of a two-step audit was valid under the
// previous rules. The migration raises the retention (logged) instead of
// leaving a body the chain would refuse to export, and archives the body as it
// was recorded.
func TestRecommitMigrationRepairsARetentionShorterThanTheAuditLife(t *testing.T) {
	f, _ := importedLiveChain(t, func(body *tasktypes.TaskParamsV1) {
		v := body.Verification
		body.Cleanup.TaskTerminalSummaryRetentionBlocks = v.AnchorOpenWindowBlocks + v.AnchorProofWindowBlocks + v.AnchorVerdictWindowBlocks + 1
	})
	body, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.Error(t, body.Validate())
	require.NoError(t, tasktypes.ValidateHistoricalTaskParams(body), "valid as the previous version recorded it")
	old := leaveStateAsConsensusVersion4(t, f)

	require.NoError(t, f.keeper.RecommitTaskParams(f.ctx))

	got, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Greater(t, got.Cleanup.TaskTerminalSummaryRetentionBlocks, got.Verification.AnchorTotalWindowBlocks())
	want := body
	want.Cleanup.TaskTerminalSummaryRetentionBlocks = got.Cleanup.TaskTerminalSummaryRetentionBlocks
	require.Equal(t, want, got, "nothing but the retention changes")
	archived, err := f.keeper.ParamsHistory.Get(f.ctx, old.ParamsVersion)
	require.NoError(t, err)
	require.Equal(t, body, archived.Params)
	require.NoError(t, f.keeper.EnsureParamsVersionInvariant(f.ctx))
	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())
}
