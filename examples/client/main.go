// Demo client: GET <server>/premium, expect 402, build & sign an x402
// Authorization, retry, print the body and on-chain tx hash.
//
// Reuse a fixed nonce by setting X402_REUSE_NONCE=1 to test replay
// rejection. Set X402_EXPIRED=1 to send an already-expired authorization
// (validBefore in the past), to test the expired_authorization path.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	bip39 "github.com/cosmos/go-bip39"

	x402cosmos "github.com/yourorg/x402-cosmos/facilitator"
)

func main() {
	serverURL := envOr("X402_SERVER_URL", "http://localhost:8080/premium")
	mnemonic := os.Getenv("X402_PAYER_MNEMONIC")
	if mnemonic == "" {
		log.Fatal("X402_PAYER_MNEMONIC is required")
	}
	prefix := envOr("X402_NOBLE_PREFIX", "noble")
	reuseNonce := os.Getenv("X402_REUSE_NONCE") == "1"
	expired := os.Getenv("X402_EXPIRED") == "1"

	priv, addr, err := deriveKey(mnemonic, prefix)
	if err != nil {
		log.Fatalf("derive payer key: %v", err)
	}
	log.Printf("payer addr: %s", addr)

	httpClient := &http.Client{Timeout: 60 * time.Second}

	// 1. First GET — expect 402.
	req, _ := http.NewRequest(http.MethodGet, serverURL, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatalf("first request: %v", err)
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		log.Fatalf("expected 402, got %d", resp.StatusCode)
	}
	reqHeader := resp.Header.Get("PAYMENT-REQUIRED")
	resp.Body.Close()
	if reqHeader == "" {
		log.Fatal("server did not return PAYMENT-REQUIRED header")
	}
	var requirements x402cosmos.PaymentRequirements
	if err := decodeHeader(reqHeader, &requirements); err != nil {
		log.Fatalf("decode requirements: %v", err)
	}
	log.Printf("requirements: scheme=%s network=%s amount=%s %s payTo=%s",
		requirements.Scheme, requirements.Network, requirements.MaxAmountRequired,
		requirements.Asset, requirements.PayTo)

	// 2. Build authorization.
	nonce := freshNonce()
	if reuseNonce {
		nonce = "0x" + strings.Repeat("ab", 32)
		log.Printf("reusing fixed nonce (replay test)")
	}
	now := time.Now().Unix()
	validAfter := uint64(now - 5)
	validBefore := uint64(now + 30)
	if expired {
		validAfter = uint64(now - 60)
		validBefore = uint64(now - 10)
		log.Printf("crafting expired authorization (validBefore in the past)")
	}
	auth := x402cosmos.Authorization{
		From:        addr,
		To:          requirements.PayTo,
		Denom:       requirements.Asset,
		Amount:      requirements.MaxAmountRequired,
		Nonce:       nonce,
		ValidAfter:  validAfter,
		ValidBefore: validBefore,
		Resource:    requirements.Resource,
		ChainID:     requirements.Extra.ChainID,
	}

	// 3. Sign via ADR-036 (the helper hashes via the secp256k1 SDK key).
	signature, pubKeyBytes, err := signAuth(priv, auth)
	if err != nil {
		log.Fatalf("sign: %v", err)
	}
	payload := x402cosmos.PaymentPayload{
		X402Version: 2,
		Scheme:      x402cosmos.Scheme,
		Network:     requirements.Network,
		Payload: x402cosmos.CosmosPayload{
			From:          addr,
			PublicKey:     base64.StdEncoding.EncodeToString(pubKeyBytes),
			Signature:     base64.StdEncoding.EncodeToString(signature),
			Authorization: auth,
		},
	}
	payloadHeader, err := encodeHeader(payload)
	if err != nil {
		log.Fatalf("encode payload: %v", err)
	}

	// 4. Retry with PAYMENT-SIGNATURE.
	req2, _ := http.NewRequest(http.MethodGet, serverURL, nil)
	req2.Header.Set("PAYMENT-SIGNATURE", payloadHeader)
	resp2, err := httpClient.Do(req2)
	if err != nil {
		log.Fatalf("paid request: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)

	respHeader := resp2.Header.Get("PAYMENT-RESPONSE")
	if respHeader != "" {
		var settle x402cosmos.SettleResponse
		if err := decodeHeader(respHeader, &settle); err == nil {
			if settle.Success {
				log.Printf("PAYMENT SETTLED tx=%s", settle.Transaction)
				log.Printf("explorer: https://www.mintscan.io/noble-testnet/txs/%s", settle.Transaction)
			} else {
				log.Printf("PAYMENT FAILED errorReason=%q", settle.ErrorReason)
			}
		}
	}

	if resp2.StatusCode != http.StatusOK {
		log.Fatalf("paid request returned status %d, body=%s", resp2.StatusCode, body)
	}
	fmt.Printf("response body: %s\n", body)
}

// signAuth produces the ADR-036 signature over `auth` and returns
// (signature, compressed-pubkey-bytes). Uses the shared preimage helper
// from the facilitator package — the only way to guarantee byte-identical
// sign-bytes between client and verifier.
func signAuth(priv cryptotypes.PrivKey, auth x402cosmos.Authorization) ([]byte, []byte, error) {
	preimage, err := x402cosmos.ADR036Preimage(auth)
	if err != nil {
		return nil, nil, err
	}
	sig, err := priv.Sign(preimage)
	if err != nil {
		return nil, nil, err
	}
	return sig, priv.PubKey().Bytes(), nil
}

func freshNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "0x" + hex.EncodeToString(b)
}

func deriveKey(mnemonic, prefix string) (cryptotypes.PrivKey, string, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, "", errors.New("invalid mnemonic")
	}
	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)
	keyBz, err := hd.DerivePrivateKeyForPath(master, ch, "m/44'/118'/0'/0/0")
	if err != nil {
		return nil, "", err
	}
	priv := &secp256k1.PrivKey{Key: keyBz}
	addr, err := bech32.ConvertAndEncode(prefix, priv.PubKey().Address().Bytes())
	if err != nil {
		return nil, "", err
	}
	return priv, addr, nil
}

func encodeHeader(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func decodeHeader(h string, out interface{}) error {
	raw, err := base64.StdEncoding.DecodeString(h)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
