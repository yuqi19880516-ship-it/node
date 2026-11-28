package keeper_test

import (
	"bytes"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/TrueOpen/node/x/hub/keeper"
	"github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
)

// registerAnchorProfileForTest registers a Profile whose evidence schema
// requires the Worker anchor commitment, bound to the given registration hash
// (nil for none).
func registerAnchorProfileForTest(t *testing.T, f *fixture, label string, registrationHash []byte) []byte {
	t.Helper()
	chainID := sdk.UnwrapSDKContext(f.ctx).ChainID()
	proposer := hubAddress(t, 250)
	projection := testModelProfileProjection(chainID, proposer, label, 1, testServiceBondMinInitial)
	schema := &projection.VerificationProfile.EvidenceSchema
	schema.RequiredInferEvidence = append(schema.RequiredInferEvidence, shared.InferEvidenceRequirementV1{
		EvidenceKind:            shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING,
		CommitmentSchemaVersion: shared.WorkerAnchorCommitmentSchemaVersionV1, MaxEncodedSizeBytes: 16 << 20,
		RegistrationHash: registrationHash,
	})
	projection.VerificationProfile.EvidenceSchemaHash = nil
	schemaHash, err := types.EvidenceSchemaHash(projection)
	require.NoError(t, err)
	projection.VerificationProfile.EvidenceSchemaHash = schemaHash
	digest, _, err := types.ModelRegistrationDigest(chainID, proposer, projection)
	require.NoError(t, err)
	_, _, _, replay, err := f.keeper.RegisterModelProfileState(f.ctx, proposer, projection, digest, testServiceBondMinInitial, types.ModelRegistrationFeeMinMicroUSDC, 5)
	require.NoError(t, err)
	require.False(t, replay)
	return projection.ModelId
}

// The registration hash set at register-model-profile is stored with the
// Profile version, returned by the Profile query and by the snapshot the Task
// module reads, and survives an export and import.
func TestProfileCarriesItsRegistrationHash(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, *types.DefaultGenesis()))
	registration := bytes.Repeat([]byte{0x5a}, 32)
	bound := registerAnchorProfileForTest(t, f, "bound-model", registration)
	plain := registerAnchorProfileForTest(t, f, "plain-model", nil)

	read := func(f *fixture, modelID []byte) []byte {
		response, err := keeper.NewQueryServerImpl(f.keeper).Profile(f.ctx, &types.QueryProfileRequest{ModelId: modelID, ProfileVersion: 1})
		require.NoError(t, err)
		fromQuery := shared.AnchorRegistrationHash(response.Profile.VerificationProfile.EvidenceSchema)
		snapshot, found := f.keeper.GetProfileState(sdk.UnwrapSDKContext(f.ctx), modelID, 1)
		require.True(t, found)
		require.Equal(t, fromQuery, shared.AnchorRegistrationHash(snapshot.ExecutionSnapshot.VerificationProfile.EvidenceSchema))
		return fromQuery
	}
	require.Equal(t, registration, read(f, bound))
	require.Nil(t, read(f, plain))

	boundSnapshot, _ := f.keeper.GetProfileState(sdk.UnwrapSDKContext(f.ctx), bound, 1)
	plainSnapshot, _ := f.keeper.GetProfileState(sdk.UnwrapSDKContext(f.ctx), plain, 1)
	require.NotEqual(t, boundSnapshot.ExecutionSnapshot.VerificationProfile.EvidenceSchemaHash, plainSnapshot.ExecutionSnapshot.VerificationProfile.EvidenceSchemaHash)

	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())
	restarted := initFixture(t)
	require.NoError(t, restarted.keeper.InitGenesis(restarted.ctx, *exported))
	require.Equal(t, registration, read(restarted, bound))
	require.Nil(t, read(restarted, plain))
}
