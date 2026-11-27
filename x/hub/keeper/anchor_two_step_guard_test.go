package keeper_test

import (
	"context"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/TrueOpen/node/x/hub/keeper"
	"github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
)

// twoStepGate is the Task-side fact the registration guard reads. It also
// satisfies the role-fault gate, the dependency the Task keeper is wired in as.
type twoStepGate struct{ configured bool }

func (twoStepGate) RoleFaultConsumersClosed(context.Context, []byte, []byte, []byte) (bool, error) {
	return true, nil
}

func (g twoStepGate) AnchorTwoStepSamplingConfigured(context.Context) (bool, error) {
	return g.configured, nil
}

// plainGate is a role-fault gate that cannot answer for the Task parameters.
type plainGate struct{}

func (plainGate) RoleFaultConsumersClosed(context.Context, []byte, []byte, []byte) (bool, error) {
	return true, nil
}

func anchorProfile(t *testing.T, chainID, proposer, label string, anchorVersion uint32) shared.ModelProfileProjection {
	t.Helper()
	profile := testModelProfileProjection(chainID, proposer, label, 1, testServiceBondMinInitial)
	profile.VerificationProfile.EvidenceSchema.RequiredInferEvidence = append(profile.VerificationProfile.EvidenceSchema.RequiredInferEvidence,
		shared.InferEvidenceRequirementV1{
			EvidenceKind:            shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING,
			CommitmentSchemaVersion: anchorVersion, MaxEncodedSizeBytes: 16 << 20,
		})
	profile.VerificationProfile.EvidenceSchemaHash = nil
	hash, err := types.EvidenceSchemaHash(profile)
	require.NoError(t, err)
	profile.VerificationProfile.EvidenceSchemaHash = hash
	return profile
}

// A Profile version that asks for two-step sampling (anchor commitment schema
// version 2) is admitted only on a chain that can run it: the VRF beacon is
// required at this height and the Task parameters of the flow are set. A
// version-1 Profile is admitted as before on any chain.
func TestRegisterTwoStepProfileNeedsVRFAndParameters(t *testing.T) {
	const chainID, denom = "two-step-chain", "uusdc"
	for name, tc := range map[string]struct {
		vrfRequiredFrom uint64
		gate            types.RoleFaultConsumerGate
		anchorVersion   uint32
		wantErr         string
	}{
		"version 1 on a chain without VRF and without parameters": {0, plainGate{}, shared.WorkerAnchorCommitmentSchemaVersionV1, ""},
		"version 2 without the VRF requirement":                   {0, twoStepGate{configured: true}, shared.WorkerAnchorCommitmentSchemaVersionV2, "vrf_required_from_height"},
		"version 2 before the VRF requirement is reached":         {26, twoStepGate{configured: true}, shared.WorkerAnchorCommitmentSchemaVersionV2, "vrf_required_from_height"},
		"version 2 without the Task parameters":                   {20, twoStepGate{configured: false}, shared.WorkerAnchorCommitmentSchemaVersionV2, "anchor_list_window_blocks"},
		"version 2 when the Task parameters cannot be read":       {20, plainGate{}, shared.WorkerAnchorCommitmentSchemaVersionV2, "cannot be read"},
		"version 2 with VRF required and the parameters set":      {20, twoStepGate{configured: true}, shared.WorkerAnchorCommitmentSchemaVersionV2, ""},
		"version 2 at exactly the VRF requirement height":         {25, twoStepGate{configured: true}, shared.WorkerAnchorCommitmentSchemaVersionV2, ""},
		"an unknown anchor schema version":                        {20, twoStepGate{configured: true}, 3, "anchor commitment V1 or V2"},
	} {
		t.Run(name, func(t *testing.T) {
			f := initFixture(t)
			genesis := types.DefaultGenesis()
			genesis.Params.Phase0.BusinessDenom = denom
			require.NoError(t, f.keeper.InitGenesis(f.ctx, *genesis))
			params, err := f.keeper.Params.Get(f.ctx)
			require.NoError(t, err)
			params.Beacon.VrfRequiredFromHeight = tc.vrfRequiredFrom
			require.NoError(t, f.keeper.Params.Set(f.ctx, params))
			registrant := hubIdentity(t, 180)
			registerHubTestAccount(t, f, registrant)
			fee := types.ModelRegistrationFeeMinMicroUSDC
			f.bank.add(registrant.Address, denom, 10*fee)
			f.ctx = sdk.WrapSDKContext(sdk.UnwrapSDKContext(f.ctx).WithChainID(chainID).WithBlockHeight(25))
			server := keeper.NewMsgServerImpl(f.keeper, types.MsgServerDependencies{RoleFaultConsumerGate: tc.gate})

			profile := anchorProfile(t, chainID, registrant.Address, "two-step-model", tc.anchorVersion)
			profile.MinStake.Denom = denom
			profile.RegistrationFee = sdk.NewCoin(denom, sdkmath.NewIntFromUint64(fee))
			before := f.bank.balance(registrant.Address, denom)
			request := signedModelProfileRequest(t, f, registrant, profile)
			response, err := server.RegisterModelProfile(f.ctx, request)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Equal(t, before, f.bank.balance(registrant.Address, denom), "a refused registration moves no fee")
				_, err := f.keeper.Profile.Get(f.ctx, types.NewProfileStateKey(profile.ModelId, profile.ProfileVersion))
				require.Error(t, err, "a refused registration writes no Profile")
				return
			}
			require.NoError(t, err)
			require.False(t, response.IdempotentReplay)

			// An accepted registration stays reachable when the chain's
			// parameters change afterwards: the replay answers before the guard.
			params.Beacon.VrfRequiredFromHeight = 0
			require.NoError(t, f.keeper.Params.Set(f.ctx, params))
			replay, err := keeper.NewMsgServerImpl(f.keeper, types.MsgServerDependencies{RoleFaultConsumerGate: plainGate{}}).RegisterModelProfile(f.ctx, request)
			require.NoError(t, err)
			require.True(t, replay.IdempotentReplay)
		})
	}
}

// CH-8. The registration guard checks the VRF requirement once. A later Hub
// parameter update could take it away, after which no position draw can be
// made and every two-step audit ends NO_VERDICT. Such an update is refused
// while a Profile version that is not delisted uses two-step sampling; it is
// accepted when no such Profile exists, and any other update is unaffected.
func TestHubParamsCannotDropVRFWhileATwoStepProfileIsListed(t *testing.T) {
	const chainID, denom = "two-step-chain", "uusdc"
	const height = int64(25)
	authority := authtypes.NewModuleAddress(types.GovModuleName).String()
	for name, tc := range map[string]struct {
		anchorVersion uint32
		delist        bool
		wantRefused   bool
	}{
		"a listed two-step Profile":   {shared.WorkerAnchorCommitmentSchemaVersionV2, false, true},
		"a delisted two-step Profile": {shared.WorkerAnchorCommitmentSchemaVersionV2, true, false},
		"only a one-step Profile":     {shared.WorkerAnchorCommitmentSchemaVersionV1, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := initFixture(t)
			genesis := types.DefaultGenesis()
			genesis.Params.Phase0.BusinessDenom = denom
			require.NoError(t, f.keeper.InitGenesis(f.ctx, *genesis))
			params, err := f.keeper.Params.Get(f.ctx)
			require.NoError(t, err)
			params.Beacon.VrfRequiredFromHeight = 20
			require.NoError(t, f.keeper.Params.Set(f.ctx, params))
			registrant := hubIdentity(t, 180)
			registerHubTestAccount(t, f, registrant)
			fee := types.ModelRegistrationFeeMinMicroUSDC
			f.bank.add(registrant.Address, denom, 10*fee)
			f.ctx = sdk.WrapSDKContext(sdk.UnwrapSDKContext(f.ctx).WithChainID(chainID).WithBlockHeight(height))
			server := keeper.NewMsgServerImpl(f.keeper, types.MsgServerDependencies{
				RoleFaultConsumerGate:     twoStepGate{configured: true},
				ValidatorSnapshotProvider: deterministicValidatorSnapshots{historicalEntries: 10_000},
				TaskSafetyWindowProvider:  staticTaskSafetyWindows{},
			})
			profile := anchorProfile(t, chainID, registrant.Address, "two-step-model", tc.anchorVersion)
			profile.MinStake.Denom = denom
			profile.RegistrationFee = sdk.NewCoin(denom, sdkmath.NewIntFromUint64(fee))
			_, err = server.RegisterModelProfile(f.ctx, signedModelProfileRequest(t, f, registrant, profile))
			require.NoError(t, err)
			if tc.delist {
				key := types.NewProfileStateKey(profile.ModelId, profile.ProfileVersion)
				stored, err := f.keeper.Profile.Get(f.ctx, key)
				require.NoError(t, err)
				stored.Status = types.ModelStatusDelisted
				require.NoError(t, f.keeper.Profile.Set(f.ctx, key, stored))
			}

			update := func(mutate func(*types.HubParamsV2)) error {
				current, err := f.keeper.Params.Get(f.ctx)
				require.NoError(t, err)
				meta, err := f.keeper.GetHubParamsMeta(f.ctx)
				require.NoError(t, err)
				next := current
				mutate(&next)
				_, err = server.UpdateHubParams(f.ctx, &types.MsgUpdateHubParams{Authority: authority, Params: next, ExpectedVersion: meta.ParamsVersion})
				return err
			}
			// An update that keeps the requirement is never touched by the guard.
			require.NoError(t, update(func(p *types.HubParamsV2) { p.Support.MaxModelSupportPruneItemsPerBlock++ }))

			for what, mutate := range map[string]func(*types.HubParamsV2){
				"removed":   func(p *types.HubParamsV2) { p.Beacon.VrfRequiredFromHeight = 0 },
				"postponed": func(p *types.HubParamsV2) { p.Beacon.VrfRequiredFromHeight = uint64(height) + 100 },
			} {
				before, err := f.keeper.Params.Get(f.ctx)
				require.NoError(t, err)
				err = update(mutate)
				if tc.wantRefused {
					require.ErrorIs(t, err, types.ErrInvalidBeacon, what)
					require.ErrorContains(t, err, "two-step anchor sampling", what)
					after, err := f.keeper.Params.Get(f.ctx)
					require.NoError(t, err)
					require.Equal(t, before, after, "a refused update changes nothing")
					continue
				}
				require.NoError(t, err, what)
				// Put the requirement back for the next case.
				restored := before
				require.NoError(t, f.keeper.Params.Set(f.ctx, restored))
			}
		})
	}
}

// CH-8. On a chain where the requirement was never reached there is nothing to
// keep: the guard does not look at Profiles at all (a genesis may carry a
// two-step Profile on such a chain; its audits end NO_VERDICT with their own
// reason).
func TestHubParamsGuardIgnoresAChainWithoutTheVRFRequirement(t *testing.T) {
	f := initFixture(t)
	require.NoError(t, f.keeper.InitGenesis(f.ctx, *types.DefaultGenesis()))
	f.ctx = sdk.WrapSDKContext(sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(25))
	server := keeper.NewMsgServerImpl(f.keeper, types.MsgServerDependencies{
		ValidatorSnapshotProvider: deterministicValidatorSnapshots{historicalEntries: 10_000},
		TaskSafetyWindowProvider:  staticTaskSafetyWindows{},
	})
	current, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.Zero(t, current.Beacon.VrfRequiredFromHeight)
	next := current
	next.Beacon.VrfRequiredFromHeight = 1_000
	_, err = server.UpdateHubParams(f.ctx, &types.MsgUpdateHubParams{
		Authority: authtypes.NewModuleAddress(types.GovModuleName).String(), Params: next,
	})
	require.NoError(t, err)
}
