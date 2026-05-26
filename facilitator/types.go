// Package x402cosmos defines the data structures for the exact_cosmos_authz
// scheme of the x402 payment protocol. See
// specs/scheme_exact_cosmos_authz.md for the full specification.
package x402cosmos

// Scheme is the x402 scheme identifier for this implementation.
const Scheme = "exact_cosmos_authz"

// PaymentRequirements is the payload a resource server returns in the
// PAYMENT-REQUIRED header of a 402 response.
type PaymentRequirements struct {
	Scheme            string                 `json:"scheme"`
	Network           string                 `json:"network"` // "cosmos:<chain-id>"
	MaxAmountRequired string                 `json:"maxAmountRequired"`
	Asset             string                 `json:"asset"`    // bank denom
	PayTo             string                 `json:"payTo"`    // bech32 recipient
	Resource          string                 `json:"resource"` // canonical URL
	Description       string                 `json:"description,omitempty"`
	MimeType          string                 `json:"mimeType,omitempty"`
	MaxTimeoutSeconds uint64                 `json:"maxTimeoutSeconds"`
	OutputSchema      map[string]interface{} `json:"outputSchema,omitempty"`
	Extra             RequirementsExtra      `json:"extra"`
}

// RequirementsExtra holds Cosmos-specific fields outside the chain-agnostic
// x402 core.
type RequirementsExtra struct {
	Facilitator string `json:"facilitator"` // bech32, grantee address
	ChainID     string `json:"chainId"`
	Decimals    uint8  `json:"decimals,omitempty"`
	Symbol      string `json:"symbol,omitempty"`
}

// PaymentPayload is the client-supplied payload, sent in PAYMENT-SIGNATURE
// header (base64-encoded JSON of this struct).
type PaymentPayload struct {
	X402Version int           `json:"x402Version"`
	Scheme      string        `json:"scheme"`
	Network     string        `json:"network"`
	Payload     CosmosPayload `json:"payload"`
}

// CosmosPayload carries the ADR-036 signature and the authorization it covers.
type CosmosPayload struct {
	From          string        `json:"from"`      // bech32, must equal Authorization.From
	PublicKey     string        `json:"publicKey"` // base64, 33-byte compressed secp256k1
	Signature     string        `json:"signature"` // base64, 64-byte r||s
	Authorization Authorization `json:"authorization"`
}

// Authorization is the structured message the payer signs. Its canonical
// JSON serialization is the `data` field inside the ADR-036 StdSignDoc.
//
// Field order in this struct does not matter for signing — canonicalJSON
// re-sorts keys lexicographically. But the JSON tag names DO matter; they
// are part of the wire format and signed bytes.
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Denom       string `json:"denom"`
	Amount      string `json:"amount"`      // atomic units, decimal string
	Nonce       string `json:"nonce"`       // 0x-prefixed hex, 32 bytes
	ValidAfter  uint64 `json:"validAfter"`  // unix seconds, inclusive
	ValidBefore uint64 `json:"validBefore"` // unix seconds, exclusive
	Resource    string `json:"resource"`
	ChainID     string `json:"chainId"`
}

// VerifyRequest is the body of POST /verify on the facilitator.
type VerifyRequest struct {
	X402Version         int                 `json:"x402Version"`
	PaymentPayload      PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements PaymentRequirements `json:"paymentRequirements"`
}

// VerifyResponse is the response body.
type VerifyResponse struct {
	IsValid       bool   `json:"isValid"`
	InvalidReason string `json:"invalidReason,omitempty"`
	Payer         string `json:"payer,omitempty"` // bech32
}

// SettleRequest mirrors VerifyRequest. Spec keeps them separate so that
// settle implementations can layer additional policy on top of verify.
type SettleRequest = VerifyRequest

// SettleResponse is the response body of POST /settle.
type SettleResponse struct {
	Success     bool   `json:"success"`
	ErrorReason string `json:"errorReason,omitempty"`
	Transaction string `json:"transaction,omitempty"` // tx hash, hex
	Network     string `json:"network,omitempty"`
	Payer       string `json:"payer,omitempty"`
}

// Standard error reason codes; see spec.
const (
	ErrInvalidSignature     = "invalid_signature"
	ErrInvalidAuthorization = "invalid_authorization"
	ErrNonceAlreadyUsed     = "nonce_already_used"
	ErrExpiredAuthorization = "expired_authorization"
	ErrInsufficientGrant    = "insufficient_grant"
	ErrInsufficientFunds    = "insufficient_funds"
	ErrBroadcastFailed      = "broadcast_failed"
	ErrUnexpected           = "unexpected_settle_error"
)
