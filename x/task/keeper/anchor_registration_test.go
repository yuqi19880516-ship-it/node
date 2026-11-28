package keeper

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

func TestAnchorRegistrationHashAccessor(t *testing.T) {
	f := newAnchorOnlyFixture(t, 1_000_000_000, true)
	core := mustCore(t, f)
	require.Nil(t, f.keeper.AnchorRegistrationHash(f.ctx, core.ModelId, core.ProfileVersion), "the Profile sets none")
	require.Nil(t, f.keeper.AnchorRegistrationHash(f.ctx, bytes32(0xee), 9), "unknown Profile")
}

type profileHub struct {
	types.HubKeeper
	profile hubtypes.ProfileStateSnapshot
}

func (h profileHub) GetProfileState(sdk.Context, []byte, uint32) (hubtypes.ProfileStateSnapshot, bool) {
	return h.profile, true
}

func TestAnchorRegistrationHashOfTheTaskProfile(t *testing.T) {
	f := newAnchorOnlyFixture(t, 1_000_000_000, true)
	core := mustCore(t, f)
	profile, found := f.keeper.hubKeeper.GetProfileState(sdkContextFrom(f.ctx), core.ModelId, core.ProfileVersion)
	require.True(t, found)
	schema := profile.ExecutionSnapshot.VerificationProfile.EvidenceSchema
	schema.RequiredInferEvidence = append([]shared.InferEvidenceRequirementV1(nil), schema.RequiredInferEvidence...)
	schema.RequiredInferEvidence[2].RegistrationHash = bytes32(0x5a)
	profile.ExecutionSnapshot.VerificationProfile.EvidenceSchema = schema
	f.keeper.hubKeeper = profileHub{HubKeeper: f.keeper.hubKeeper, profile: profile}
	require.Equal(t, bytes32(0x5a), f.keeper.AnchorRegistrationHash(f.ctx, core.ModelId, core.ProfileVersion))
}
