package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/TrueOpen/node/x/hub/types"
)

// RecommitParams writes the current Hub parameter body as a new params
// version when its recorded commitment no longer matches the body, which is
// the case after an upgrade to a binary whose parameter commitment frames more
// fields. The body is not changed. A store whose commitment already matches,
// or that never recorded one, is left alone.
func (k Keeper) RecommitParams(ctx context.Context) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	meta, err := k.GetHubParamsMeta(ctx)
	if err != nil {
		return err
	}
	if meta.ParamsVersion == 0 {
		return nil
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	current, err := types.HubParamsHash(sdkCtx.ChainID(), meta.ParamsVersion, params)
	if err != nil {
		return err
	}
	if bytes.Equal(current, meta.ParamsHash) {
		return nil
	}
	version, err := checkedAdd(meta.ParamsVersion, 1)
	if err != nil {
		return errorsmod.Wrap(types.ErrInvariantBroken, "params_version overflow")
	}
	hash, err := types.HubParamsHash(sdkCtx.ChainID(), version, params)
	if err != nil {
		return err
	}
	next := types.HubParamsMetaState{ParamsVersion: version, ParamsHash: hash, UpdatedHeight: sdkContextHeight(sdkCtx)}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := k.ParamsMeta.Set(ctx, next); err != nil {
		return err
	}
	mustEmitHubEvent(ctx, &types.EventHubParamsUpdated{OldVersion: meta.ParamsVersion, NewVersion: version, ParamsHash: append([]byte(nil), hash...)})
	return nil
}
