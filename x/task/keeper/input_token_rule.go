package keeper

import (
	"bytes"
	"context"
	"errors"

	"cosmossdk.io/collections"

	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

// recordInputTokenStatement keeps the input token count the user attested for
// a task being recorded. The statement is optional and outside the signed
// dispatch record:
//
//   - with input_token_tolerance zero in the task's parameters every
//     attestation is ignored;
//   - an order without an attestation (a takeover never carries one) is
//     recorded as it is and the input-token rule does not apply to that task;
//   - an attestation that does not verify (another chain, order or signer, a
//     signature that is not the EIP-712 form) is dropped the same way.
//
// None of these refuses the dispatch record or is anybody's fault.
func (k Keeper) recordInputTokenStatement(ctx context.Context, admission dispatchOrderAdmission, params types.TaskParamsV1, attestations []*types.OrderInputTokensAttestationV1) error {
	if params.Verification.InputTokenTolerance == 0 {
		return nil
	}
	var statement *types.OrderInputTokensAttestationV1
	for _, attestation := range attestations {
		if attestation == nil || !bytes.Equal(attestation.OrderDigest, admission.taskHash[:]) {
			continue
		}
		if statement != nil {
			// Two statements for one order: neither is taken.
			return nil
		}
		statement = attestation
	}
	if statement == nil {
		return nil
	}
	if err := types.VerifyOrderInputTokensAttestation(*statement, sdkContextFrom(ctx).ChainID(), admission.taskHash[:], admission.signed, admission.signer); err != nil {
		return nil
	}
	return k.TaskInputTokenStatement.Set(ctx, admission.taskID[:], statement.InputTokenCount)
}

// inputTokenRuleViolated reports whether a receipt states fewer prompt tokens
// than the user attested, beyond the tolerance. The prompt token count is read
// from the Worker token commitment; a receipt without one, or a task without
// a statement, is outside the rule.
func (k Keeper) inputTokenRuleViolated(ctx context.Context, taskKey types.TaskKey, params types.TaskParamsV1, receipt types.InferReceiptV3) (bool, error) {
	tolerance := params.Verification.InputTokenTolerance
	if tolerance == 0 {
		return false, nil
	}
	attested, err := k.TaskInputTokenStatement.Get(ctx, taskKey)
	if errors.Is(err, collections.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, commitment := range receipt.RequiredEvidenceCommitments {
		if commitment.EvidenceKind != shared.EvidenceKind_EVIDENCE_KIND_WORKER_TOKEN_OPENING {
			continue
		}
		prompt, ok := types.PromptTokenCount(commitment.EncodedSizeBytes, receipt.GeneratedTokenCount)
		return ok && types.InputTokenRuleViolated(attested, prompt, tolerance), nil
	}
	return false, nil
}

// inputTokenRefusalStands reports whether a receipt of the task was refused
// under the input-token rule. A Worker whose receipt was refused that way did
// answer in time, so the inference deadline that follows is not its fault.
func (k Keeper) inputTokenRefusalStands(ctx context.Context, taskKey types.TaskKey, params types.TaskParamsV1) (bool, error) {
	if params.Verification.InputTokenTolerance == 0 {
		return false, nil
	}
	return k.TaskInputTokenRefusal.Has(ctx, taskKey)
}

// forgetInputTokenStatement removes the rule's rows of a task that reached
// finality.
func (k Keeper) forgetInputTokenStatement(ctx context.Context, taskKey types.TaskKey, params types.TaskParamsV1) error {
	if params.Verification.InputTokenTolerance == 0 {
		return nil
	}
	if err := k.TaskInputTokenStatement.Remove(ctx, taskKey); err != nil {
		return err
	}
	return k.TaskInputTokenRefusal.Remove(ctx, taskKey)
}
