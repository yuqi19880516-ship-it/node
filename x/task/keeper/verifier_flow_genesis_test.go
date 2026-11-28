package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	tasktypes "github.com/TrueOpen/node/x/task/types"
)

// Brake rows survive an export and import. The withheld slashes themselves are
// not in the exported genesis, so an imported row carries no pending effect.
func TestAnchorBrakeGenesisRoundTrip(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, *tasktypes.DefaultGenesis()))
	model := repeatByte(0x91)
	rows := []tasktypes.AnchorBrakeState{
		{ModelId: model, Period: 3, Audited: 40, Cases: 1, RateBps: 250},
		{ModelId: model, Period: 4, Audited: 12, Cases: 6, RateBps: 5_000, Tripped: true, TrippedHeight: 77, BaselineBps: 250, PendingEffects: 4},
		{ModelId: repeatByte(0x92), Period: 4, Audited: 9, Cases: 3, RateBps: 3_333, Tripped: true, TrippedHeight: 80, Released: true, Applied: true, ReleasedHeight: 90},
	}
	for _, row := range rows {
		require.NoError(t, f.keeper.AnchorBrake.Set(f.ctx, collections.Join(tasktypes.NewTaskKey(row.ModelId), row.Period), row))
	}
	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, rows, exported.AnchorBrakes)
	require.NoError(t, exported.Validate())

	restarted := initFixture(t)
	require.NoError(t, restarted.keeper.InitGenesis(restarted.ctx, *exported))
	reexported, err := restarted.keeper.ExportGenesis(restarted.ctx)
	require.NoError(t, err)
	want := append([]tasktypes.AnchorBrakeState(nil), rows...)
	want[1].PendingEffects = 0
	require.Equal(t, want, reexported.AnchorBrakes)

	duplicated := *exported
	duplicated.AnchorBrakes = append(duplicated.AnchorBrakes, rows[0])
	third := initFixture(t)
	require.ErrorContains(t, third.keeper.InitGenesis(third.ctx, duplicated), "duplicate anchor brake row")
}

// A chain that never set the new parameters exports no row of the brake, and
// the input-token rows are never part of a genesis.
func TestVerifierFlowRowsAreAbsentFromADefaultGenesis(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, *tasktypes.DefaultGenesis()))
	require.NoError(t, f.keeper.TaskInputTokenStatement.Set(f.ctx, tasktypes.NewTaskKey(repeatByte(0x93)), 17))
	require.NoError(t, f.keeper.TaskInputTokenRefusal.Set(f.ctx, tasktypes.NewTaskKey(repeatByte(0x93))))
	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Empty(t, exported.AnchorBrakes)

	restarted := initFixture(t)
	require.NoError(t, restarted.keeper.InitGenesis(restarted.ctx, *exported))
	has, err := restarted.keeper.TaskInputTokenStatement.Has(restarted.ctx, tasktypes.NewTaskKey(repeatByte(0x93)))
	require.NoError(t, err)
	require.False(t, has, "a statement is not carried across an import: the rule does not apply to that task afterwards")
}
