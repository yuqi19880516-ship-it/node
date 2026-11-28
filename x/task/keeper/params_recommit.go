package keeper

import (
	"bytes"
	"context"
	"math"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/TrueOpen/node/x/task/types"
)

// RecommitParams writes the current parameter body as a new params version
// when its recorded commitment no longer matches the body. That is the case
// after an upgrade to a binary whose parameter commitment frames more fields:
// the body is unchanged, every new field is zero, but the commitment of the
// same body is a different value. The replaced version is archived with its
// body, so objects bound to it keep reading it. A store whose commitment
// already matches, or that never recorded one, is left alone.
func (k Keeper) RecommitParams(ctx context.Context) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	meta, err := k.GetTaskParamsMeta(ctx)
	if err != nil {
		return err
	}
	if meta.ParamsVersion == 0 {
		return nil
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	current, err := types.TaskParamsHashV1(sdkCtx.ChainID(), meta.ParamsVersion, params)
	if err != nil {
		return err
	}
	if bytes.Equal(current, meta.ParamsHash) {
		return nil
	}
	if meta.ParamsVersion == math.MaxUint64 {
		return errorsmod.Wrap(types.ErrInvariantBroken, "params_version overflow")
	}
	version := meta.ParamsVersion + 1
	hash, err := types.TaskParamsHashV1(sdkCtx.ChainID(), version, params)
	if err != nil {
		return err
	}
	height, err := currentBlockHeight(ctx)
	if err != nil {
		return err
	}
	if err := k.archiveParamsForUpdate(ctx, meta.ParamsVersion, params); err != nil {
		return err
	}
	if err := k.ParamsMeta.Set(ctx, types.TaskParamsMetaState{ParamsVersion: version, ParamsHash: hash, UpdatedHeight: height}); err != nil {
		return err
	}
	return emitTypedEvent(ctx, &types.EventTaskParamsUpdated{OldVersion: meta.ParamsVersion, NewVersion: version, ParamsHash: hash})
}
