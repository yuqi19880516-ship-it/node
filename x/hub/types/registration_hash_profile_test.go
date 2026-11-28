package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	shared "github.com/TrueOpen/node/x/shared/types"
)

func projectionWithAnchor(t *testing.T, registrationHash []byte) shared.ModelProfileProjection {
	t.Helper()
	projection := goldenModelProfileProjection(t)
	schema := projection.VerificationProfile.EvidenceSchema
	schema.RequiredInferEvidence = append(append([]shared.InferEvidenceRequirementV1(nil), schema.RequiredInferEvidence...), shared.InferEvidenceRequirementV1{
		EvidenceKind:            shared.EvidenceKind_EVIDENCE_KIND_WORKER_ANCHOR_OPENING,
		CommitmentSchemaVersion: shared.WorkerAnchorCommitmentSchemaVersionV1, MaxEncodedSizeBytes: 16 << 20,
		RegistrationHash: registrationHash,
	})
	projection.VerificationProfile.EvidenceSchema = schema
	projection.VerificationProfile.EvidenceSchemaHash = nil
	hash, err := EvidenceSchemaHash(projection)
	require.NoError(t, err)
	projection.VerificationProfile.EvidenceSchemaHash = hash
	return projection
}

// The registration hash is part of the Profile's evidence schema hash and of
// the registration body when it is set, and absent from both when it is not.
func TestRegistrationHashEntersTheProfileCommitments(t *testing.T) {
	registration := bytes.Repeat([]byte{0x5a}, 32)
	plain, bound := projectionWithAnchor(t, nil), projectionWithAnchor(t, registration)
	require.NotEqual(t, plain.VerificationProfile.EvidenceSchemaHash, bound.VerificationProfile.EvidenceSchemaHash)

	other := projectionWithAnchor(t, bytes.Repeat([]byte{0x5b}, 32))
	require.NotEqual(t, bound.VerificationProfile.EvidenceSchemaHash, other.VerificationProfile.EvidenceSchemaHash)

	plainBody, err := CanonicalModelProfileProjection(plain)
	require.NoError(t, err)
	require.NotContains(t, string(plainBody), "registration_hash")
	boundBody, err := CanonicalModelProfileProjection(bound)
	require.NoError(t, err)
	require.Contains(t, string(boundBody), `"registration_hash":"0x`+"5a5a5a5a")

	// A stale schema hash is refused: the hash must cover the registration.
	stale := bound
	stale.VerificationProfile.EvidenceSchemaHash = plain.VerificationProfile.EvidenceSchemaHash
	_, err = CanonicalModelProfileProjection(stale)
	require.ErrorContains(t, err, "evidence_schema_hash does not match")
}

func TestRegistrationHashInTheProfileJSON(t *testing.T) {
	registration := bytes.Repeat([]byte{0x5a}, 32)
	for _, hash := range [][]byte{nil, registration} {
		projection := projectionWithAnchor(t, hash)
		parsed, err := ParseModelProfileProjectionJSON(marshalModelProfileProjectionJSON(t, projection))
		require.NoError(t, err)
		require.Equal(t, hash, shared.AnchorRegistrationHash(parsed.VerificationProfile.EvidenceSchema))
		want, err := CanonicalModelProfileProjection(projection)
		require.NoError(t, err)
		got, err := CanonicalModelProfileProjection(parsed)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	body := marshalModelProfileProjectionJSON(t, projectionWithAnchor(t, registration))
	hexValue := []byte(testHashHex(registration))
	for _, row := range []struct {
		name, replacement string
	}{
		{"not hex", `0xzz` + string(hexValue[4:])},
		{"upper case", "0x" + string(bytes.ToUpper(hexValue[2:]))},
		{"no prefix", string(hexValue[2:]) + "00"},
		{"zero", "0x" + string(bytes.Repeat([]byte{'0'}, 64))},
	} {
		_, err := ParseModelProfileProjectionJSON(bytes.Replace(body, hexValue, []byte(row.replacement), 1))
		require.Error(t, err, row.name)
	}
}
