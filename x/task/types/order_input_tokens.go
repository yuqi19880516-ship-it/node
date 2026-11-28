package types

import (
	"bytes"
	"fmt"
	"math"

	shared "github.com/TrueOpen/node/x/shared/types"
)

// The user's statement of an order's input token count travels beside the
// order. Its digest is TRUEOPEN_ORDER_INPUT_TOKENS_V1 over (schema_version,
// chain_id, order_digest, input_token_count); its signature is EIP-712 typed
// data that carries that digest, made with the key that signed the order.
const (
	OrderInputTokensSchemaVersionV1     = uint32(1)
	OrderInputTokensEIP712DomainName    = "TrueOpen Order Input Tokens"
	OrderInputTokensEIP712DomainVersion = "1"
	orderInputTokensEIP712MessageType   = "OrderInputTokens(string chainId,bytes32 orderDigest,uint64 inputTokenCount,bytes32 attestationDigest)"
)

// OrderInputTokensDigest is the wire digest of an attestation.
func OrderInputTokensDigest(attestation OrderInputTokensAttestationV1) ([32]byte, error) {
	var zero [32]byte
	if attestation.SchemaVersion != OrderInputTokensSchemaVersionV1 {
		return zero, fmt.Errorf("input token attestation schema_version must be %d", OrderInputTokensSchemaVersionV1)
	}
	if attestation.ChainId == "" || len(attestation.OrderDigest) != Hash32Len {
		return zero, fmt.Errorf("input token attestation needs a chain_id and a 32-byte order_digest")
	}
	digest, err := shared.NewCanonicalHashBuilderV1(shared.MustDomain(shared.DomainOrderInputTokensV1)).Raw(
		shared.Uint32BE(attestation.SchemaVersion),
		[]byte(attestation.ChainId),
		attestation.OrderDigest,
		shared.Uint64BE(attestation.InputTokenCount),
	).Sum()
	if err != nil {
		return zero, err
	}
	return [32]byte(digest), nil
}

// BuildOrderInputTokensEIP712Digest derives the typed-data digest the user
// signs: domain {name, version, chainId = signature_chain_id} and the primary
// type OrderInputTokens carrying the wire digest.
func BuildOrderInputTokensEIP712Digest(attestation OrderInputTokensAttestationV1) (TaskOrderEIP712Digest, error) {
	if attestation.SignatureChainId == 0 || attestation.SignatureChainId > math.MaxInt64 {
		return TaskOrderEIP712Digest{}, fmt.Errorf("signature_chain_id must be in 1..%d", int64(math.MaxInt64))
	}
	wireDigest, err := OrderInputTokensDigest(attestation)
	if err != nil {
		return TaskOrderEIP712Digest{}, err
	}
	domainTypeHash := eip712Keccak256([]byte(taskOrderEIP712DomainType))
	domainSeparator := eip712Keccak256(
		domainTypeHash[:],
		eip712StringWord(OrderInputTokensEIP712DomainName),
		eip712StringWord(OrderInputTokensEIP712DomainVersion),
		eip712Uint64Word(attestation.SignatureChainId),
	)
	messageTypeHash := eip712Keccak256([]byte(orderInputTokensEIP712MessageType))
	hashStruct := eip712Keccak256(
		messageTypeHash[:],
		eip712StringWord(attestation.ChainId),
		attestation.OrderDigest,
		eip712Uint64Word(attestation.InputTokenCount),
		wireDigest[:],
	)
	return TaskOrderEIP712Digest{
		DomainSeparator: domainSeparator,
		HashStruct:      hashStruct,
		SigningDigest:   eip712Keccak256([]byte{0x19, 0x01}, domainSeparator[:], hashStruct[:]),
	}, nil
}

// VerifyOrderInputTokensAttestation checks one attestation against the order
// it claims to belong to: same chain, the order's digest, the order's
// signature scheme and signature chain id, and a canonical EIP-712 signature
// whose recovered address is the address that signed the order. A signature
// over the bare wire digest does not verify.
func VerifyOrderInputTokensAttestation(attestation OrderInputTokensAttestationV1, chainID string, orderDigest []byte, order SignedOrderV2, orderSigner []byte) error {
	if attestation.ChainId != chainID || !bytes.Equal(attestation.OrderDigest, orderDigest) {
		return fmt.Errorf("input token attestation names another chain or order")
	}
	if attestation.SignatureScheme != SignatureSchemeEIP712 || order.SignatureScheme != SignatureSchemeEIP712 ||
		attestation.SignatureChainId != order.SignatureChainId {
		return fmt.Errorf("input token attestation does not use the order's signature scheme and chain id")
	}
	digest, err := BuildOrderInputTokensEIP712Digest(attestation)
	if err != nil {
		return err
	}
	recovered, err := RecoverEIP712Signer(digest.SigningDigest[:], attestation.UserSignature)
	if err != nil {
		return err
	}
	if len(orderSigner) == 0 || !bytes.Equal(recovered.Address, orderSigner) {
		return fmt.Errorf("input token attestation is not signed by the order's signer")
	}
	return nil
}

// InputTokenRuleViolated is the receipt rule: with the Worker's prompt token
// count read from its token commitment, a receipt is refused when
// prompt_token_count < attested - tolerance. A zero tolerance switches the
// rule off.
func InputTokenRuleViolated(attested, promptTokenCount, tolerance uint64) bool {
	if tolerance == 0 || attested <= tolerance {
		return false
	}
	return promptTokenCount < attested-tolerance
}

// PromptTokenCount derives the Worker's input token count from its token
// commitment size S and generated token count G: (S - 8) / 4 - G, where each
// token-id artifact is a four-byte count followed by four bytes per token. ok
// is false when S is not such a pair covering G.
func PromptTokenCount(tokenCommitmentSizeBytes, generatedTokenCount uint64) (uint64, bool) {
	if tokenCommitmentSizeBytes < 8 || (tokenCommitmentSizeBytes-8)%4 != 0 {
		return 0, false
	}
	total := (tokenCommitmentSizeBytes - 8) / 4
	if total < generatedTokenCount {
		return 0, false
	}
	return total - generatedTokenCount, true
}
