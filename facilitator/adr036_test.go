package x402cosmos

import (
	"encoding/json"
	"testing"
)

// TestCanonicalAuthorizationJSON pins the exact wire bytes for a known
// authorization. If this test breaks, signature verification will break
// on the chain side too. Treat it as a wire-format contract.
func TestCanonicalAuthorizationJSON(t *testing.T) {
	a := Authorization{
		From:        "noble1payerxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		To:          "noble1recipientxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		Denom:       "uusdc",
		Amount:      "10000",
		Nonce:       "0xdeadbeef",
		ValidAfter:  1716700000,
		ValidBefore: 1716700060,
		Resource:    "https://api.example.com/v1/premium",
		ChainID:     "noble-1",
	}

	bz, err := CanonicalAuthorizationJSON(a)
	if err != nil {
		t.Fatalf("CanonicalAuthorizationJSON: %v", err)
	}

	// Keys must appear in lexicographic order: amount, chainId, denom,
	// from, nonce, resource, to, validAfter, validBefore.
	want := `{"amount":"10000","chainId":"noble-1","denom":"uusdc","from":"noble1payerxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","nonce":"0xdeadbeef","resource":"https://api.example.com/v1/premium","to":"noble1recipientxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","validAfter":1716700000,"validBefore":1716700060}`

	if got := string(bz); got != want {
		t.Errorf("canonical JSON mismatch\n got: %s\nwant: %s", got, want)
	}

	// Sanity: re-parsing must round-trip.
	var rt Authorization
	if err := json.Unmarshal(bz, &rt); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if rt != a {
		t.Errorf("round-trip mismatch: %+v vs %+v", rt, a)
	}
}

func TestADR036SignBytesDeterministic(t *testing.T) {
	a := Authorization{
		From: "noble1payer", To: "noble1recv", Denom: "uusdc", Amount: "1",
		Nonce: "0x01", ValidAfter: 1, ValidBefore: 2,
		Resource: "https://x/y", ChainID: "noble-1",
	}
	h1, err := ADR036SignBytes(a)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ADR036SignBytes(a)
	if err != nil {
		t.Fatal(err)
	}
	if string(h1) != string(h2) {
		t.Error("ADR036SignBytes is not deterministic")
	}
	if len(h1) != 32 {
		t.Errorf("expected SHA-256 digest (32 bytes), got %d", len(h1))
	}
}
