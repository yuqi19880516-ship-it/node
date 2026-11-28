package types

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// inputTokensVector is the SDK's published vector of the EIP-712 form of
// OrderInputTokensAttestationV1 (order_input_tokens_eip712_v1.json), built on
// the values of the wire vector order_input_tokens_v1.json.
type inputTokensVector struct {
	Domain struct {
		DomainSeparator string `json:"domain_separator"`
		TypeHash        string `json:"type_hash"`
	} `json:"domain"`
	EncodeType    string `json:"encode_type"`
	TypeHash      string `json:"type_hash"`
	HashStruct    string `json:"hash_struct"`
	SigningDigest string `json:"signing_digest"`
	Message       struct {
		AttestationDigest string `json:"attestationDigest"`
	} `json:"message"`
	Signer struct {
		EthAddress string `json:"eth_address"`
	} `json:"signer"`
	Attestation struct {
		SchemaVersion    uint32 `json:"schema_version"`
		ChainID          string `json:"chain_id"`
		OrderDigest      string `json:"order_digest"`
		InputTokenCount  string `json:"input_token_count"`
		SignatureScheme  string `json:"signature_scheme"`
		UserSignature    string `json:"user_signature"`
		SignatureChainID string `json:"signature_chain_id"`
	} `json:"attestation"`
	NegativeCases []struct {
		Name             string `json:"name"`
		SignatureChainID string `json:"signature_chain_id"`
		InputTokenCount  string `json:"input_token_count"`
		SigningDigest    string `json:"signing_digest"`
	} `json:"negative_cases"`
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	require.NoError(t, err)
	return raw
}

func mustUint(t *testing.T, value string) uint64 {
	t.Helper()
	parsed, err := strconv.ParseUint(value, 10, 64)
	require.NoError(t, err)
	return parsed
}

func loadInputTokensVector(t *testing.T) (inputTokensVector, OrderInputTokensAttestationV1) {
	t.Helper()
	raw, err := os.ReadFile("testdata/order_input_tokens_eip712_v1.json")
	require.NoError(t, err)
	var vector inputTokensVector
	require.NoError(t, json.Unmarshal(raw, &vector))
	return vector, OrderInputTokensAttestationV1{
		SchemaVersion: vector.Attestation.SchemaVersion, ChainId: vector.Attestation.ChainID,
		OrderDigest: mustHex(t, vector.Attestation.OrderDigest), InputTokenCount: mustUint(t, vector.Attestation.InputTokenCount),
		SignatureScheme: vector.Attestation.SignatureScheme, UserSignature: mustHex(t, vector.Attestation.UserSignature),
		SignatureChainId: mustUint(t, vector.Attestation.SignatureChainID),
	}
}

func TestOrderInputTokensMatchesThePublishedVector(t *testing.T) {
	vector, attestation := loadInputTokensVector(t)
	require.Equal(t, vector.EncodeType, orderInputTokensEIP712MessageType)
	typeHash := eip712Keccak256([]byte(orderInputTokensEIP712MessageType))
	require.Equal(t, vector.TypeHash, hex.EncodeToString(typeHash[:]))
	domainTypeHash := eip712Keccak256([]byte(taskOrderEIP712DomainType))
	require.Equal(t, vector.Domain.TypeHash, hex.EncodeToString(domainTypeHash[:]))

	wireDigest, err := OrderInputTokensDigest(attestation)
	require.NoError(t, err)
	require.Equal(t, vector.Message.AttestationDigest, hex.EncodeToString(wireDigest[:]), "the wire digest of order_input_tokens_v1.json")
	require.Equal(t, "d05f13e5b7ec9888eeff687c6a6554180e6c45e3d66a5c0c523456fb234344b0", hex.EncodeToString(wireDigest[:]))

	digest, err := BuildOrderInputTokensEIP712Digest(attestation)
	require.NoError(t, err)
	require.Equal(t, vector.Domain.DomainSeparator, hex.EncodeToString(digest.DomainSeparator[:]))
	require.Equal(t, vector.HashStruct, hex.EncodeToString(digest.HashStruct[:]))
	require.Equal(t, vector.SigningDigest, hex.EncodeToString(digest.SigningDigest[:]))

	recovered, err := RecoverEIP712Signer(digest.SigningDigest[:], attestation.UserSignature)
	require.NoError(t, err)
	require.Equal(t, vector.Signer.EthAddress, hex.EncodeToString(recovered.Address))

	for _, negative := range vector.NegativeCases {
		changed := attestation
		if negative.SignatureChainID != "" {
			changed.SignatureChainId = mustUint(t, negative.SignatureChainID)
		}
		if negative.InputTokenCount != "" {
			changed.InputTokenCount = mustUint(t, negative.InputTokenCount)
		}
		other, err := BuildOrderInputTokensEIP712Digest(changed)
		require.NoError(t, err, negative.Name)
		require.Equal(t, negative.SigningDigest, hex.EncodeToString(other.SigningDigest[:]), negative.Name)
		signer, err := RecoverEIP712Signer(other.SigningDigest[:], attestation.UserSignature)
		if err == nil {
			require.NotEqual(t, vector.Signer.EthAddress, hex.EncodeToString(signer.Address), negative.Name)
		}
	}
}

func TestVerifyOrderInputTokensAttestationRows(t *testing.T) {
	vector, good := loadInputTokensVector(t)
	signer := mustHex(t, vector.Signer.EthAddress)
	order := SignedOrderV2{SignatureScheme: SignatureSchemeEIP712, SignatureChainId: good.SignatureChainId}
	verify := func(attestation OrderInputTokensAttestationV1, order SignedOrderV2, signer []byte) error {
		return VerifyOrderInputTokensAttestation(attestation, "trueopen-localnet-1", good.OrderDigest, order, signer)
	}
	require.NoError(t, verify(good, order, signer))

	for _, row := range []struct {
		name   string
		mutate func(*OrderInputTokensAttestationV1, *SignedOrderV2, *[]byte)
	}{
		{"count changed after signing", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.InputTokenCount++ }},
		{"another chain", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.ChainId = "trueopen-localnet-2" }},
		{"another order", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) {
			a.OrderDigest = append([]byte{a.OrderDigest[0] ^ 1}, a.OrderDigest[1:]...)
		}},
		{"schema version", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.SchemaVersion = 2 }},
		{"signature scheme", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.SignatureScheme = "eip191" }},
		{"empty scheme", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.SignatureScheme = "" }},
		{"signature chain id differs from the order's", func(_ *OrderInputTokensAttestationV1, o *SignedOrderV2, _ *[]byte) { o.SignatureChainId = 1 }},
		{"both signature chain ids changed", func(a *OrderInputTokensAttestationV1, o *SignedOrderV2, _ *[]byte) {
			a.SignatureChainId, o.SignatureChainId = 1, 1
		}},
		{"zero signature chain id", func(a *OrderInputTokensAttestationV1, o *SignedOrderV2, _ *[]byte) {
			a.SignatureChainId, o.SignatureChainId = 0, 0
		}},
		{"another signer", func(_ *OrderInputTokensAttestationV1, _ *SignedOrderV2, s *[]byte) {
			*s = append([]byte{(*s)[0] ^ 1}, (*s)[1:]...)
		}},
		{"no known signer", func(_ *OrderInputTokensAttestationV1, _ *SignedOrderV2, s *[]byte) { *s = nil }},
		{"missing signature", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) { a.UserSignature = nil }},
		{"64-byte signature", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) {
			a.UserSignature = a.UserSignature[:64]
		}},
		{"recovery id 0/1 instead of 27/28", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) {
			a.UserSignature = append(append([]byte(nil), a.UserSignature[:64]...), a.UserSignature[64]-27)
		}},
		{"high S", func(a *OrderInputTokensAttestationV1, _ *SignedOrderV2, _ *[]byte) {
			a.UserSignature = append([]byte(nil), a.UserSignature...)
			for i := 32; i < 64; i++ {
				a.UserSignature[i] = 0xff
			}
		}},
	} {
		attestation, signedOrder, expected := good, order, append([]byte(nil), signer...)
		row.mutate(&attestation, &signedOrder, &expected)
		require.Error(t, verify(attestation, signedOrder, expected), row.name)
	}
}

// A signature made over the bare wire digest, without the typed-data
// envelope, is refused.
func TestOrderInputTokensRefusesASignatureOverTheBareDigest(t *testing.T) {
	vector, attestation := loadInputTokensVector(t)
	wireDigest, err := OrderInputTokensDigest(attestation)
	require.NoError(t, err)
	signer, err := RecoverEIP712Signer(wireDigest[:], attestation.UserSignature)
	if err == nil {
		require.NotEqual(t, vector.Signer.EthAddress, hex.EncodeToString(signer.Address))
	}
	typed, err := BuildOrderInputTokensEIP712Digest(attestation)
	require.NoError(t, err)
	require.NotEqual(t, wireDigest, typed.SigningDigest)
}

func TestInputTokenRuleRows(t *testing.T) {
	for _, row := range []struct {
		name                        string
		attested, prompt, tolerance uint64
		violated                    bool
	}{
		{"rule off", 100, 0, 0, false},
		{"equal", 100, 100, 5, false},
		{"prompt above the statement", 100, 400, 5, false},
		{"inside the tolerance", 100, 95, 5, false},
		{"one below the tolerance", 100, 94, 5, true},
		{"tolerance covers the whole statement", 5, 0, 5, false},
		{"tolerance above the statement", 3, 0, 5, false},
		{"zero prompt", 100, 0, 5, true},
		{"largest statement", ^uint64(0), ^uint64(0) - 6, 5, true},
	} {
		require.Equal(t, row.violated, InputTokenRuleViolated(row.attested, row.prompt, row.tolerance), row.name)
	}
}

func TestPromptTokenCountRows(t *testing.T) {
	for _, row := range []struct {
		name            string
		size, generated uint64
		want            uint64
		ok              bool
	}{
		{"published example", 28, 3, 2, true},
		{"only generated tokens", 20, 3, 0, true},
		{"too small", 7, 0, 0, false},
		{"not a multiple of four", 30, 3, 0, false},
		{"fewer tokens than generated", 16, 3, 0, false},
		{"empty pair", 8, 0, 0, true},
	} {
		got, ok := PromptTokenCount(row.size, row.generated)
		require.Equal(t, row.ok, ok, row.name)
		require.Equal(t, row.want, got, row.name)
	}
}

// The brake and the input-token rule are off in the default parameters, and
// the suggested brake values are a valid parameter body.
func TestVerifierFlowRuleParametersDefaultToOff(t *testing.T) {
	v := DefaultTaskParams().Verification
	require.Zero(t, v.InputTokenTolerance)
	require.Zero(t, v.AnchorBrakeMinCases)
	require.Zero(t, v.AnchorBrakeMultipleX1000)
	require.Zero(t, v.AnchorBrakeBaselinePeriods)
	require.Zero(t, v.BatchSettlementPeriodBlocks)

	params := DefaultTaskParams()
	params.Verification.AnchorBrakeMinCases = RecommendedAnchorBrakeMinCases
	params.Verification.AnchorBrakeMultipleX1000 = RecommendedAnchorBrakeMultipleX1000
	params.Verification.AnchorBrakeBaselinePeriods = RecommendedAnchorBrakeBaselinePeriods
	require.NoError(t, params.Validate())
	params.Verification.AnchorBrakeBaselinePeriods = MaxAnchorBrakeBaselinePeriods
	require.NoError(t, params.Validate())
	params.Verification.AnchorBrakeBaselinePeriods = MaxAnchorBrakeBaselinePeriods + 1
	require.ErrorContains(t, params.Validate(), "anchor_brake_baseline_periods")
}
