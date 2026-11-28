package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	shared "github.com/TrueOpen/node/x/shared/types"
)

func anchorRequirement(registrationHash []byte) shared.InferEvidenceRequirementV1 {
	return shared.InferEvidenceRequirementV1{
		EvidenceKind:            shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING,
		CommitmentSchemaVersion: shared.WorkerAnchorCommitmentSchemaVersionV1, MaxEncodedSizeBytes: 16 << 20,
		RegistrationHash: registrationHash,
	}
}

func schemaWithAnchor(registrationHash []byte) shared.EvidenceSchemaV1 {
	schema := shared.NewPhase0WorkerEvidenceSchemaV1(1<<30, 1<<26)
	schema.RequiredInferEvidence = append(schema.RequiredInferEvidence, anchorRequirement(registrationHash))
	return schema
}

// An entry without a registration hash is framed with the three fields it
// always had; an entry that sets one commits to it as a fourth field.
func TestEvidenceRequirementFrameWithAndWithoutRegistrationHash(t *testing.T) {
	registration := bytes.Repeat([]byte{0x5a}, 32)
	plain, err := shared.CanonicalInferEvidenceRequirementTypedFrameV1(anchorRequirement(nil)).Bytes()
	require.NoError(t, err)
	legacy, err := shared.FlatCanonicalFrameV1(
		shared.EnumBE(uint32(shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING)),
		shared.Uint32BE(shared.WorkerAnchorCommitmentSchemaVersionV1), shared.Uint64BE(16<<20),
	).Bytes()
	require.NoError(t, err)
	require.Equal(t, legacy, plain)

	bound, err := shared.CanonicalInferEvidenceRequirementTypedFrameV1(anchorRequirement(registration)).Bytes()
	require.NoError(t, err)
	extended, err := shared.FlatCanonicalFrameV1(
		shared.EnumBE(uint32(shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING)),
		shared.Uint32BE(shared.WorkerAnchorCommitmentSchemaVersionV1), shared.Uint64BE(16<<20), registration,
	).Bytes()
	require.NoError(t, err)
	require.Equal(t, extended, bound)
	require.NotEqual(t, plain, bound)

	other, err := shared.CanonicalInferEvidenceRequirementTypedFrameV1(anchorRequirement(bytes.Repeat([]byte{0x5b}, 32))).Bytes()
	require.NoError(t, err)
	require.NotEqual(t, bound, other, "another registration is another schema")

	plainSchema, err := shared.CanonicalEvidenceSchemaFrameV1(schemaWithAnchor(nil))
	require.NoError(t, err)
	boundSchema, err := shared.CanonicalEvidenceSchemaFrameV1(schemaWithAnchor(registration))
	require.NoError(t, err)
	require.NotEqual(t, plainSchema, boundSchema)
}

func TestEvidenceRegistrationHashValidationRows(t *testing.T) {
	registration := bytes.Repeat([]byte{0x5a}, 32)
	require.NoError(t, shared.ValidateEvidenceSchemaV1(schemaWithAnchor(nil)))
	require.NoError(t, shared.ValidateEvidenceSchemaV1(schemaWithAnchor(registration)))
	for _, row := range []struct {
		name string
		hash []byte
	}{
		{"31 bytes", registration[:31]},
		{"33 bytes", append(bytes.Clone(registration), 1)},
		{"all zero", make([]byte, 32)},
	} {
		require.Error(t, shared.ValidateEvidenceSchemaV1(schemaWithAnchor(row.hash)), row.name)
		require.Error(t, shared.CanonicalInferEvidenceRequirementTypedFrameV1(anchorRequirement(row.hash)).Err(), row.name)
	}
	onTokens := shared.NewPhase0WorkerEvidenceSchemaV1(1<<30, 1<<26)
	onTokens.RequiredInferEvidence[1].RegistrationHash = registration
	require.ErrorContains(t, shared.ValidateEvidenceSchemaV1(onTokens), "only valid on the Worker anchor commitment")
}

func TestAnchorRegistrationHashOfASchema(t *testing.T) {
	registration := bytes.Repeat([]byte{0x5a}, 32)
	require.Nil(t, shared.AnchorRegistrationHash(shared.NewPhase0WorkerEvidenceSchemaV1(1<<30, 1<<26)), "no anchor commitment")
	require.Nil(t, shared.AnchorRegistrationHash(schemaWithAnchor(nil)), "anchor commitment without a registration")
	got := shared.AnchorRegistrationHash(schemaWithAnchor(registration))
	require.Equal(t, registration, got)
	got[0] ^= 1
	require.Equal(t, registration, shared.AnchorRegistrationHash(schemaWithAnchor(registration)), "the result is a copy")
}
