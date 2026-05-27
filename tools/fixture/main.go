// Command fixture produces a signed x402-cosmos /settle request body
// suitable for replay against gateways like suverse-pay.
//
// Unlike examples/client (which talks to a paid demo server over HTTP),
// this tool only emits JSON. It signs the Authorization with the payer's
// ADR-036 key, embeds the matching PaymentRequirements, and writes the
// {paymentPayload, paymentRequirements} pair that resource gateways
// expect on POST /settle.
//
// Each invocation generates a fresh random nonce, so the resulting
// fixture is single-use on chain. Re-run to refresh.
//
// Usage:
//
//	go run ./tools/fixture \
//	    --payer-mnemonic "$X402_PAYER_MNEMONIC" \
//	    --recipient "$X402_PAY_TO" \
//	    --facilitator "$X402_FACILITATOR_GRANTEE" \
//	    --amount 10000 \
//	    --output /path/to/signed-settle-fresh.json
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
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
	payerMnemonic := flag.String("payer-mnemonic", os.Getenv("X402_PAYER_MNEMONIC"), "Payer BIP-39 mnemonic (or X402_PAYER_MNEMONIC)")
	recipient := flag.String("recipient", os.Getenv("X402_PAY_TO"), "Recipient bech32 (or X402_PAY_TO)")
	facilitator := flag.String("facilitator", os.Getenv("X402_FACILITATOR_GRANTEE"), "Facilitator/grantee bech32 (or X402_FACILITATOR_GRANTEE)")
	amount := flag.String("amount", envOr("X402_AMOUNT", "10000"), "Amount in atomic units (uusdc)")
	asset := flag.String("asset", envOr("X402_ASSET", "uusdc"), "Asset denom")
	network := flag.String("network", envOr("X402_NETWORK", "cosmos:grand-1"), "x402 network identifier")
	chainID := flag.String("chain-id", envOr("X402_NOBLE_CHAIN_ID", "grand-1"), "Cosmos chain-id")
	prefix := flag.String("prefix", envOr("X402_NOBLE_PREFIX", "noble"), "Bech32 prefix")
	resource := flag.String("resource", "https://suverse-pay.example/v1/smoke", "Canonical resource URL the authorization binds to")
	validitySeconds := flag.Int64("validity-seconds", 50, "Total validity window (validAfter=now-5, validBefore=now+this). Must stay <= PaymentRequirements.maxTimeoutSeconds.")
	maxTimeoutSeconds := flag.Uint64("max-timeout-seconds", 60, "PaymentRequirements.maxTimeoutSeconds upper bound")
	output := flag.String("output", "-", "Output path, '-' for stdout")
	flag.Parse()

	if *payerMnemonic == "" {
		log.Fatal("--payer-mnemonic (or X402_PAYER_MNEMONIC) is required")
	}
	if *recipient == "" {
		log.Fatal("--recipient (or X402_PAY_TO) is required")
	}
	if *facilitator == "" {
		log.Fatal("--facilitator (or X402_FACILITATOR_GRANTEE) is required")
	}

	priv, payerAddr, err := deriveKey(*payerMnemonic, *prefix)
	if err != nil {
		log.Fatalf("derive payer key: %v", err)
	}

	now := time.Now().Unix()
	auth := x402cosmos.Authorization{
		From:        payerAddr,
		To:          *recipient,
		Denom:       *asset,
		Amount:      *amount,
		Nonce:       freshNonce(),
		ValidAfter:  uint64(now - 5),
		ValidBefore: uint64(now + *validitySeconds),
		Resource:    *resource,
		ChainID:     *chainID,
	}

	preimage, err := x402cosmos.ADR036Preimage(auth)
	if err != nil {
		log.Fatalf("preimage: %v", err)
	}
	signature, err := priv.Sign(preimage)
	if err != nil {
		log.Fatalf("sign: %v", err)
	}

	payload := x402cosmos.PaymentPayload{
		X402Version: 2,
		Scheme:      x402cosmos.Scheme,
		Network:     *network,
		Payload: x402cosmos.CosmosPayload{
			From:          payerAddr,
			PublicKey:     base64.StdEncoding.EncodeToString(priv.PubKey().Bytes()),
			Signature:     base64.StdEncoding.EncodeToString(signature),
			Authorization: auth,
		},
	}

	requirements := x402cosmos.PaymentRequirements{
		Scheme:            x402cosmos.Scheme,
		Network:           *network,
		MaxAmountRequired: *amount,
		Asset:             *asset,
		PayTo:             *recipient,
		Resource:          *resource,
		MaxTimeoutSeconds: *maxTimeoutSeconds,
		Extra: x402cosmos.RequirementsExtra{
			Facilitator: *facilitator,
			ChainID:     *chainID,
		},
	}

	fixture := struct {
		PaymentPayload      x402cosmos.PaymentPayload      `json:"paymentPayload"`
		PaymentRequirements x402cosmos.PaymentRequirements `json:"paymentRequirements"`
	}{payload, requirements}

	out, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	if *output == "-" {
		fmt.Println(string(out))
	} else {
		if err := os.WriteFile(*output, append(out, '\n'), 0o644); err != nil {
			log.Fatalf("write %s: %v", *output, err)
		}
		log.Printf("wrote %s (payer=%s nonce=%s validBefore=%d)", *output, payerAddr, auth.Nonce, auth.ValidBefore)
	}
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

func freshNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "0x" + hex.EncodeToString(b)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
