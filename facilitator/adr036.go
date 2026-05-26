package x402cosmos

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// adr036SignDoc is the wrapper structure ADR-036 specifies for arbitrary
// message signing. Fields chosen here mirror Keplr's `signArbitrary`
// behavior so payments can be signed by browser wallets without prompting
// the user with a "this is a transaction" dialog.
type adr036SignDoc struct {
	AccountNumber string          `json:"account_number"`
	ChainID       string          `json:"chain_id"`
	Fee           adr036Fee       `json:"fee"`
	Memo          string          `json:"memo"`
	Msgs          []adr036SignMsg `json:"msgs"`
	Sequence      string          `json:"sequence"`
}

type adr036Fee struct {
	Amount []interface{} `json:"amount"`
	Gas    string        `json:"gas"`
}

type adr036SignMsg struct {
	Type  string             `json:"type"`
	Value adr036SignMsgValue `json:"value"`
}

type adr036SignMsgValue struct {
	Data   string `json:"data"`   // base64 of the canonical Authorization JSON
	Signer string `json:"signer"` // bech32 payer
}

// CanonicalAuthorizationJSON serializes the Authorization struct as
// canonical JSON: keys sorted lexicographically at every level, no
// whitespace. This is the byte sequence that gets base64-wrapped into the
// ADR-036 `data` field, and what the payer's wallet will display.
func CanonicalAuthorizationJSON(a Authorization) ([]byte, error) {
	bz, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("marshal authorization: %w", err)
	}
	// sdk.SortJSON re-parses and re-emits with sorted keys.
	sorted, err := sdk.SortJSON(bz)
	if err != nil {
		return nil, fmt.Errorf("sort authorization json: %w", err)
	}
	return sorted, nil
}

// ADR036SignBytes returns the exact byte sequence the payer signs over.
// It is SHA-256 of the canonical-JSON-encoded ADR-036 StdSignDoc whose
// single message contains base64(canonical Authorization JSON).
//
// IMPORTANT: this function must produce byte-for-byte identical output to
// whatever the wallet computes, or signature verification fails. If you
// are debugging signature mismatches, log the raw sortedDoc bytes from
// both sides and diff them.
func ADR036SignBytes(a Authorization) ([]byte, error) {
	preimage, err := adr036Preimage(a)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(preimage)
	return sum[:], nil
}

// VerifyADR036 verifies a signature over the given authorization and
// returns the recovered bech32 address (using accountPrefix, e.g. "noble"
// for noble-1, "cosmos" for cosmoshub-4, "osmo" for osmosis-1).
//
// Errors:
//   - if publicKey is not a valid 33-byte compressed secp256k1 pubkey
//   - if signature is not 64 bytes (r||s)
//   - if signature does not verify against the sign bytes
//   - if bech32(publicKey) does not equal authorization.From
func VerifyADR036(pubKeyB64, signatureB64 string, a Authorization, accountPrefix string) (string, error) {
	pubKeyBz, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode pubkey: %w", err)
	}
	if len(pubKeyBz) != secp256k1.PubKeySize {
		return "", fmt.Errorf("pubkey length: got %d, want %d", len(pubKeyBz), secp256k1.PubKeySize)
	}
	pk := &secp256k1.PubKey{Key: pubKeyBz}

	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return "", fmt.Errorf("decode signature: %w", err)
	}
	if len(sig) != 64 {
		return "", fmt.Errorf("signature length: got %d, want 64", len(sig))
	}

	// secp256k1.PubKey.VerifySignature internally re-hashes its input with
	// SHA-256. We pass the pre-image (sortedDoc bytes) rather than the
	// digest.
	preimage, err := adr036Preimage(a)
	if err != nil {
		return "", err
	}

	if !pk.VerifySignature(preimage, sig) {
		return "", errors.New("signature verification failed")
	}

	addr, err := bech32.ConvertAndEncode(accountPrefix, pk.Address().Bytes())
	if err != nil {
		return "", fmt.Errorf("bech32 encode: %w", err)
	}
	if addr != a.From {
		return "", fmt.Errorf("pubkey/from mismatch: signed by %s, claims %s", addr, a.From)
	}
	return addr, nil
}

// ADR036Preimage returns the sorted sign-doc JSON bytes (NOT the SHA-256
// digest). VerifySignature in cosmos-sdk's secp256k1 hashes internally,
// so this is what you feed to PrivKey.Sign on the client side.
// Clients MUST use this helper to avoid drifting from the verifier.
func ADR036Preimage(a Authorization) ([]byte, error) { return adr036Preimage(a) }

func adr036Preimage(a Authorization) ([]byte, error) {
	innerJSON, err := CanonicalAuthorizationJSON(a)
	if err != nil {
		return nil, err
	}
	b64data := base64.StdEncoding.EncodeToString(innerJSON)

	doc := adr036SignDoc{
		AccountNumber: "0",
		ChainID:       "",
		Fee:           adr036Fee{Amount: []interface{}{}, Gas: "0"},
		Memo:          "",
		Msgs: []adr036SignMsg{{
			Type:  "sign/MsgSignData",
			Value: adr036SignMsgValue{Data: b64data, Signer: a.From},
		}},
		Sequence: "0",
	}
	bz, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return sdk.SortJSON(bz)
}
