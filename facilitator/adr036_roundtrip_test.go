package x402cosmos

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// TestSignAndVerifyRoundTrip generates a fresh secp256k1 key, signs an
// Authorization the same way the client does (via ADR036Preimage +
// priv.Sign), then verifies via VerifyADR036. Catches any drift between
// the client signing path and the server verification path before we ever
// touch a real chain.
func TestSignAndVerifyRoundTrip(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pubBz := priv.PubKey().Bytes()

	addr, err := bech32.ConvertAndEncode("noble", priv.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("bech32: %v", err)
	}

	auth := Authorization{
		From:        addr,
		To:          "noble1recipientxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		Denom:       "uusdc",
		Amount:      "10000",
		Nonce:       "0xdeadbeefcafebabe1234567890abcdef0fedcba98765432101122334455667788",
		ValidAfter:  1716700000,
		ValidBefore: 1716700060,
		Resource:    "https://api.example.com/v1/premium",
		ChainID:     "grand-1",
	}

	preimage, err := ADR036Preimage(auth)
	if err != nil {
		t.Fatalf("preimage: %v", err)
	}
	sig, err := priv.Sign(preimage)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	recovered, err := VerifyADR036(
		base64.StdEncoding.EncodeToString(pubBz),
		base64.StdEncoding.EncodeToString(sig),
		auth,
		"noble",
	)
	if err != nil {
		t.Fatalf("VerifyADR036: %v", err)
	}
	if recovered != addr {
		t.Errorf("recovered addr %s != signer addr %s", recovered, addr)
	}
}
