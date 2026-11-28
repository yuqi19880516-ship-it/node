package keeper

import (
	"errors"
	"testing"

	"cosmossdk.io/collections"
	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/require"

	hubtypes "github.com/TrueOpen/node/x/hub/types"
	shared "github.com/TrueOpen/node/x/shared/types"
	"github.com/TrueOpen/node/x/task/types"
)

func withInputTokenTolerance(p *types.TaskParamsV1) { p.Verification.InputTokenTolerance = 2 }

// inputTokenStatement is the attestation the SDK sends beside an order: the
// wire digest wrapped in EIP-712 typed data and signed by the order's key.
func inputTokenStatement(t *testing.T, signed types.SignedOrderV2, count uint64, key *secp.PrivateKey) *types.OrderInputTokensAttestationV1 {
	t.Helper()
	orderDigest, err := types.TaskOrderHash(signed.Order)
	require.NoError(t, err)
	attestation := &types.OrderInputTokensAttestationV1{
		SchemaVersion: types.OrderInputTokensSchemaVersionV1, ChainId: signed.Order.ChainId, OrderDigest: orderDigest[:],
		InputTokenCount: count, SignatureScheme: types.SignatureSchemeEIP712, SignatureChainId: signed.SignatureChainId,
	}
	signInputTokenStatement(t, attestation, key)
	return attestation
}

func signInputTokenStatement(t *testing.T, attestation *types.OrderInputTokensAttestationV1, key *secp.PrivateKey) {
	t.Helper()
	digest, err := types.BuildOrderInputTokensEIP712Digest(*attestation)
	require.NoError(t, err)
	compact := ecdsa.SignCompact(key, digest.SigningDigest[:], false)
	attestation.UserSignature = append(append([]byte(nil), compact[1:]...), compact[0])
}

func (f *dispatchFixture) recordWithStatements(t *testing.T, signed types.SignedOrderV2, statements ...*types.OrderInputTokensAttestationV1) (*types.DispatchEntryV1, *types.BatchItemResultV1) {
	t.Helper()
	entry := f.entry(t, signed, f.hub.builders[0])
	response, err := NewMsgServerImpl(f.keeper).RecordDispatch(f.ctx, &types.MsgRecordDispatch{
		SubmitterAddress: f.hub.builders[0], Entries: []*types.DispatchRecordEntryV1{dispatchEntryItem(entry)}, InputTokensAttestations: statements,
	})
	require.NoError(t, err)
	return entry, response.Results[0]
}

func (f *dispatchFixture) storedStatement(t *testing.T, entry *types.DispatchEntryV1) (uint64, bool) {
	t.Helper()
	count, err := f.keeper.TaskInputTokenStatement.Get(f.ctx, entry.Record.TaskId)
	if errors.Is(err, collections.ErrNotFound) {
		return 0, false
	}
	require.NoError(t, err)
	return count, true
}

// A statement signed by the order's key, in the EIP-712 form, is kept with
// the task.
func TestInputTokenStatementIsKeptWithTheTask(t *testing.T) {
	for _, key := range []string{"session key", "owner"} {
		f := newDispatchFixtureAtHeight(t, 121, withInputTokenTolerance)
		signer := f.delegateKey
		if key == "owner" {
			signer = f.ownerKey
		}
		signed := f.signedOrder(t, 0, signer)
		entry, result := f.recordWithStatements(t, signed, inputTokenStatement(t, signed, 41, signer))
		require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, result.Status, key)
		count, found := f.storedStatement(t, entry)
		require.True(t, found, key)
		require.Equal(t, uint64(41), count, key)
	}
}

// With input_token_tolerance at zero a valid statement is ignored: nothing is
// stored and the record is what it was before the rule existed.
func TestInputTokenStatementIsIgnoredAtZeroTolerance(t *testing.T) {
	f := newDispatchFixture(t)
	signed := f.signedOrder(t, 0, f.delegateKey)
	entry, result := f.recordWithStatements(t, signed, inputTokenStatement(t, signed, 41, f.delegateKey))
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, result.Status)
	_, found := f.storedStatement(t, entry)
	require.False(t, found)
}

// A record without a statement, or with one that does not verify, is accepted
// as it is. The statement is dropped; the dispatch record is not refused and
// nobody is faulted. This is the case of an order recorded by a Builder that
// took it over, which never has the statement.
func TestMissingOrInvalidInputTokenStatementNeverRefusesTheRecord(t *testing.T) {
	other := secp.PrivKeyFromBytes(bytes32(0x77))
	for _, row := range []struct {
		name  string
		build func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1
	}{
		{"no statement", func(*testing.T, *dispatchFixture, types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return nil
		}},
		{"nil statement", func(*testing.T, *dispatchFixture, types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return []*types.OrderInputTokensAttestationV1{nil}
		}},
		{"signed by another key", func(t *testing.T, _ *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return []*types.OrderInputTokensAttestationV1{inputTokenStatement(t, signed, 41, other)}
		}},
		{"signed by the owner while the session key signed the order", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return []*types.OrderInputTokensAttestationV1{inputTokenStatement(t, signed, 41, f.ownerKey)}
		}},
		{"names another order", func(t *testing.T, f *dispatchFixture, _ types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return []*types.OrderInputTokensAttestationV1{inputTokenStatement(t, f.signedOrder(t, 5, f.delegateKey), 41, f.delegateKey)}
		}},
		{"count changed after signing", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			statement := inputTokenStatement(t, signed, 41, f.delegateKey)
			statement.InputTokenCount = 4
			return []*types.OrderInputTokensAttestationV1{statement}
		}},
		{"another chain", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			statement := inputTokenStatement(t, signed, 41, f.delegateKey)
			statement.ChainId = "another-chain"
			signInputTokenStatement(t, statement, f.delegateKey)
			return []*types.OrderInputTokensAttestationV1{statement}
		}},
		{"another signature chain id than the order's", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			statement := inputTokenStatement(t, signed, 41, f.delegateKey)
			statement.SignatureChainId = 2
			signInputTokenStatement(t, statement, f.delegateKey)
			return []*types.OrderInputTokensAttestationV1{statement}
		}},
		{"signature over the bare wire digest", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			statement := inputTokenStatement(t, signed, 41, f.delegateKey)
			bare, err := types.OrderInputTokensDigest(*statement)
			require.NoError(t, err)
			compact := ecdsa.SignCompact(f.delegateKey, bare[:], false)
			statement.UserSignature = append(append([]byte(nil), compact[1:]...), compact[0])
			return []*types.OrderInputTokensAttestationV1{statement}
		}},
		{"another signature scheme", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			statement := inputTokenStatement(t, signed, 41, f.delegateKey)
			statement.SignatureScheme = "personal_sign"
			return []*types.OrderInputTokensAttestationV1{statement}
		}},
		{"two statements for one order", func(t *testing.T, f *dispatchFixture, signed types.SignedOrderV2) []*types.OrderInputTokensAttestationV1 {
			return []*types.OrderInputTokensAttestationV1{inputTokenStatement(t, signed, 41, f.delegateKey), inputTokenStatement(t, signed, 42, f.delegateKey)}
		}},
	} {
		f := newDispatchFixtureAtHeight(t, 121, withInputTokenTolerance)
		signed := f.signedOrder(t, 0, f.delegateKey)
		entry, result := f.recordWithStatements(t, signed, row.build(t, f, signed)...)
		require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, result.Status, row.name)
		_, found := f.storedStatement(t, entry)
		require.False(t, found, row.name)
		require.Empty(t, f.hub.roleFaults, row.name)
		has, err := f.keeper.DispatchWinnerIneligible.Has(f.ctx, entry.Accept.DispatchTicketHash)
		require.NoError(t, err)
		require.False(t, has, row.name)
	}
}

// A takeover entry registers the takeover and nothing else, with or without a
// statement in the message.
func TestTakeoverEntryCarriesNoInputTokenStatement(t *testing.T) {
	f := newDispatchFixtureAtHeight(t, 121, withInputTokenTolerance)
	signed := f.signedOrder(t, 0, f.delegateKey)
	response, err := NewMsgServerImpl(f.keeper).RecordDispatch(f.ctx, &types.MsgRecordDispatch{
		SubmitterAddress:        f.hub.builders[1],
		Entries:                 []*types.DispatchRecordEntryV1{{Entry: &types.DispatchRecordEntryV1_Takeover{Takeover: &types.TakeoverEntryV1{SignedOrder: &signed}}}},
		InputTokensAttestations: []*types.OrderInputTokensAttestationV1{inputTokenStatement(t, signed, 41, f.delegateKey)},
	})
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, response.Results[0].Status)
	taskID, err := types.DeriveTaskIDFromRawSession(signed.Order.SessionId, signed.Order.OrderSequence)
	require.NoError(t, err)
	has, err := f.keeper.TaskInputTokenStatement.Has(f.ctx, taskID[:])
	require.NoError(t, err)
	require.False(t, has)
	require.Empty(t, f.hub.roleFaults)
}

// The receipt of the anchor-only fixture commits 5 prompt and 10 generated
// tokens.
func TestInputTokenRuleAtTheReceiptRows(t *testing.T) {
	for _, row := range []struct {
		name      string
		tolerance uint64
		statement uint64
		refused   bool
	}{
		{"rule off, statement far above", 0, 100, false},
		{"no statement", 2, 0, false},
		{"statement equal to the prompt", 2, anchorOnlyPrompt, false},
		{"statement below the prompt", 2, 1, false},
		{"statement above, inside the tolerance", 2, anchorOnlyPrompt + 2, false},
		{"statement one beyond the tolerance", 2, anchorOnlyPrompt + 3, true},
	} {
		f := newAnchorOnlyFixture(t, 1_000_000_000, true)
		f.params.Verification.InputTokenTolerance = row.tolerance
		require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
		if row.statement != 0 {
			require.NoError(t, f.keeper.TaskInputTokenStatement.Set(f.ctx, f.taskKey, row.statement))
		}
		response, err := f.server.SubmitInferReceipt(f.at(int64(f.receiptAt)), f.receiptMsg(t))
		require.NoError(t, err, row.name)
		core := mustCore(t, f)
		refusal, err := f.keeper.TaskInputTokenRefusal.Has(f.ctx, f.taskKey)
		require.NoError(t, err)
		if !row.refused {
			require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, response.Status, row.name)
			require.False(t, refusal, row.name)
			continue
		}
		require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_REJECTED, response.Status, row.name)
		require.True(t, refusal, row.name)
		require.Equal(t, types.TaskPhase_TASK_PHASE_INFER_PENDING, core.TaskPhase, "a refused receipt is not accepted")
		has, err := f.keeper.InferReceipt.Has(f.ctx, f.taskKey)
		require.NoError(t, err)
		require.False(t, has)
		require.Empty(t, *f.faults, "the refusal itself is not a fault")
	}
}

func (f *anchorOnlyFixture) timeOutInference(t *testing.T) preVerificationFailureResult {
	t.Helper()
	core := mustCore(t, f)
	result, err := f.keeper.writePreVerificationFailure(f.at(101), core, 101, types.DeadlineKindV1_DEADLINE_KIND_V1_WORKER_INFER, types.TaskFailureClass_TASK_FAILURE_CLASS_NONE)
	require.NoError(t, err)
	require.True(t, result.Applied)
	return result
}

// A Worker whose receipt was refused under the rule answered in time. When the
// inference deadline then passes the task ends refunded and the Worker is not
// faulted; the rule's rows are removed with the task's finality.
func TestRefusedReceiptIsNotAnInferenceTimeout(t *testing.T) {
	f := newAnchorOnlyFixture(t, 1_000_000_000, true)
	f.params.Verification.InputTokenTolerance = 2
	require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
	require.NoError(t, f.keeper.TaskInputTokenStatement.Set(f.ctx, f.taskKey, 1_000))
	response, err := f.server.SubmitInferReceipt(f.at(int64(f.receiptAt)), f.receiptMsg(t))
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_REJECTED, response.Status)

	result := f.timeOutInference(t)
	require.Empty(t, *f.faults, "no fault for a Worker whose receipt was refused under the input-token rule")
	require.Equal(t, shared.TaskFinalityStatusV1_TASK_FINALITY_STATUS_V1_FINAL, result.Core.FinalityStatus)
	require.Equal(t, shared.NewAmount(f.reserved), result.RefundAmount)
	for _, has := range []func() (bool, error){
		func() (bool, error) { return f.keeper.TaskInputTokenStatement.Has(f.ctx, f.taskKey) },
		func() (bool, error) { return f.keeper.TaskInputTokenRefusal.Has(f.ctx, f.taskKey) },
	} {
		present, err := has()
		require.NoError(t, err)
		require.False(t, present)
	}
}

// The existing rule is unchanged where the input-token rule does not apply: a
// Worker that sends no receipt is faulted for the inference timeout, with or
// without a statement on the task, and also when a refusal marker is left over
// while the tolerance is zero.
func TestInferenceTimeoutFaultIsUnchangedOutsideTheRule(t *testing.T) {
	for _, row := range []struct {
		name      string
		tolerance uint64
		statement bool
		marker    bool
	}{
		{"rule off", 0, false, false},
		{"rule off, rows left over", 0, true, true},
		{"rule on, no receipt at all", 2, true, false},
	} {
		f := newAnchorOnlyFixture(t, 1_000_000_000, true)
		f.params.Verification.InputTokenTolerance = row.tolerance
		require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
		if row.statement {
			require.NoError(t, f.keeper.TaskInputTokenStatement.Set(f.ctx, f.taskKey, 1_000))
		}
		if row.marker {
			require.NoError(t, f.keeper.TaskInputTokenRefusal.Set(f.ctx, f.taskKey))
		}
		f.timeOutInference(t)
		require.Len(t, *f.faults, 1, row.name)
		require.Equal(t, hubtypes.FaultTypeWorkerInferTimeout, (*f.faults)[0].FaultType, row.name)
	}
}

// After a refusal the Worker may still send a receipt that satisfies the rule
// before the deadline; it is accepted and the task goes on.
func TestReceiptAfterARefusalIsAccepted(t *testing.T) {
	f := newAnchorOnlyFixture(t, 1_000_000_000, true)
	f.params.Verification.InputTokenTolerance = 2
	require.NoError(t, f.keeper.Params.Set(f.ctx, f.params))
	require.NoError(t, f.keeper.TaskInputTokenStatement.Set(f.ctx, f.taskKey, anchorOnlyPrompt+3))
	response, err := f.server.SubmitInferReceipt(f.at(int64(f.receiptAt)), f.receiptMsg(t))
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_REJECTED, response.Status)

	// One more committed prompt token brings the receipt inside the tolerance.
	msg := f.receiptMsg(t)
	msg.Receipt.RequiredEvidenceCommitments[1].EncodedSizeBytes += 4
	response, err = f.server.SubmitInferReceipt(f.at(int64(f.receiptAt)), msg)
	require.NoError(t, err)
	require.Equal(t, shared.MutationStatusV1_MUTATION_STATUS_V1_APPLIED, response.Status)
}
