package x402cosmos

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Verifier checks a PaymentPayload against PaymentRequirements without
// touching chain state via Verify, and (in /verify endpoint usage) also
// performs the chain-grant query.
type Verifier struct {
	chains map[string]ChainClient // network ("cosmos:<chain-id>") -> client
	nonces NonceStore
	clock  func() time.Time
}

// NewVerifier returns a Verifier bound to the given chain clients (keyed
// by full network string like "cosmos:noble-1") and nonce store.
func NewVerifier(chains map[string]ChainClient, store NonceStore) *Verifier {
	return &Verifier{
		chains: chains,
		nonces: store,
		clock:  time.Now,
	}
}

// Verify runs the full spec verification and returns the payer address on
// success. The returned error's message is suitable for the spec's
// `invalidReason` / `errorReason` field.
func (v *Verifier) Verify(ctx context.Context, req VerifyRequest) (payer string, err error) {
	pp := req.PaymentPayload
	pr := req.PaymentRequirements
	a := pp.Payload.Authorization

	// Step 1: scheme and network.
	if pp.Scheme != Scheme || pr.Scheme != Scheme {
		return "", verifyErr(ErrInvalidAuthorization, "scheme mismatch")
	}
	if pp.Network != pr.Network {
		return "", verifyErr(ErrInvalidAuthorization, "network mismatch")
	}
	client, ok := v.chains[pp.Network]
	if !ok {
		return "", verifyErr(ErrInvalidAuthorization, "unsupported network: "+pp.Network)
	}

	// Step 2-6: authorization vs requirements consistency.
	if a.To != pr.PayTo {
		return "", verifyErr(ErrInvalidAuthorization, "to/payTo mismatch")
	}
	if a.Denom != pr.Asset {
		return "", verifyErr(ErrInvalidAuthorization, "denom/asset mismatch")
	}
	if cmpAmounts(a.Amount, pr.MaxAmountRequired) > 0 {
		return "", verifyErr(ErrInvalidAuthorization, "amount exceeds maxAmountRequired")
	}
	if a.Resource != pr.Resource {
		return "", verifyErr(ErrInvalidAuthorization, "resource mismatch")
	}
	expectedChainID := strings.TrimPrefix(pp.Network, "cosmos:")
	if a.ChainID != expectedChainID || a.ChainID != client.ChainID() {
		return "", verifyErr(ErrInvalidAuthorization, "chainId mismatch")
	}

	// Step 7: validity window.
	now := v.clock().Unix()
	if uint64(now) < a.ValidAfter {
		return "", verifyErr(ErrExpiredAuthorization, "not yet valid")
	}
	if uint64(now) >= a.ValidBefore {
		return "", verifyErr(ErrExpiredAuthorization, "expired")
	}
	if a.ValidBefore <= a.ValidAfter {
		return "", verifyErr(ErrInvalidAuthorization, "non-positive validity window")
	}
	if pr.MaxTimeoutSeconds > 0 && a.ValidBefore-a.ValidAfter > pr.MaxTimeoutSeconds {
		return "", verifyErr(ErrInvalidAuthorization, "validity window exceeds maxTimeoutSeconds")
	}

	// Step 8: nonce not seen. /verify only checks; /settle calls Reserve.
	used, err := v.nonces.Exists(ctx, a.From, a.ChainID, a.Nonce)
	if err != nil {
		return "", verifyErr(ErrUnexpected, "nonce store error: "+err.Error())
	}
	if used {
		return "", verifyErr(ErrNonceAlreadyUsed, "nonce already observed")
	}

	// Step 9-10: signature + pubkey -> from binding.
	addr, err := VerifyADR036(pp.Payload.PublicKey, pp.Payload.Signature, a, client.AccountPrefix())
	if err != nil {
		return "", verifyErr(ErrInvalidSignature, err.Error())
	}

	// Step 11: chain query — grant exists, covers amount, not expired.
	limit, exp, err := client.QuerySendAuthorization(ctx, a.From, pr.Extra.Facilitator, a.Denom)
	if err != nil {
		if errors.Is(err, ErrNoGrant) {
			return "", verifyErr(ErrInsufficientGrant, "no SendAuthorization for facilitator")
		}
		return "", verifyErr(ErrUnexpected, "grant query: "+err.Error())
	}
	if cmpAmounts(limit, a.Amount) < 0 {
		return "", verifyErr(ErrInsufficientGrant, fmt.Sprintf("grant limit %s < amount %s", limit, a.Amount))
	}
	if !exp.IsZero() && !exp.After(v.clock()) {
		return "", verifyErr(ErrInsufficientGrant, "grant expired")
	}

	// Optional: balance check. The grant could be larger than the actual
	// balance. This catches the common "user revoked or moved funds"
	// case before we spend gas on a doomed broadcast.
	bal, err := client.QueryBalance(ctx, a.From, a.Denom)
	if err == nil && cmpAmounts(bal, a.Amount) < 0 {
		return "", verifyErr(ErrInsufficientFunds, fmt.Sprintf("balance %s < amount %s", bal, a.Amount))
	}

	return addr, nil
}

// verifyError carries a spec error code alongside a debug message. Use
// errors.As to extract the code.
type verifyError struct {
	Code    string
	Message string
}

func (e *verifyError) Error() string { return e.Code + ": " + e.Message }

func verifyErr(code, msg string) error { return &verifyError{Code: code, Message: msg} }

// ErrorCode extracts the spec error code from an error returned by Verify.
// Returns ErrUnexpected for non-verifyError errors.
func ErrorCode(err error) string {
	var ve *verifyError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return ErrUnexpected
}

// cmpAmounts compares two decimal-string atomic amounts. Returns -1, 0, +1.
// Both inputs MUST be non-negative integer strings (no decimals, no sign).
func cmpAmounts(a, b string) int {
	ai, _ := new(big.Int).SetString(a, 10)
	bi, _ := new(big.Int).SetString(b, 10)
	if ai == nil || bi == nil {
		// Treat unparseable as zero; verify will reject elsewhere.
		return 0
	}
	return ai.Cmp(bi)
}
