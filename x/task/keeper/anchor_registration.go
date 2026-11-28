package keeper

import (
	"context"

	shared "github.com/TrueOpen/node/x/shared/types"
)

// AnchorRegistrationHash returns the registration hash of a Profile version:
// the sha256 of the canonical registration.json its anchor audit is verified
// against, as registered in the Profile's evidence schema. It is nil when the
// Profile sets none.
func (k Keeper) AnchorRegistrationHash(ctx context.Context, modelID []byte, profileVersion uint32) []byte {
	profile, found := k.hubKeeper.GetProfileState(sdkContextFrom(ctx), modelID, profileVersion)
	if !found {
		return nil
	}
	return shared.AnchorRegistrationHash(profile.ExecutionSnapshot.VerificationProfile.EvidenceSchema)
}
