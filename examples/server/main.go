// Demo resource server: exposes GET /premium for 0.01 USDC on Noble testnet.
// Configure via env vars; see README.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/yourorg/x402-cosmos/middleware"
)

func main() {
	addr := envOr("X402_SERVER_ADDR", ":8080")
	payTo := mustEnv("X402_PAY_TO")
	facilitatorURL := envOr("X402_FACILITATOR_URL", "http://localhost:8402")
	facilitatorAddr := mustEnv("X402_FACILITATOR_GRANTEE")
	amount := envOr("X402_AMOUNT", "10000") // 0.01 USDC (6 decimals)
	network := envOr("X402_NETWORK", "cosmos:grand-1")
	asset := envOr("X402_ASSET", "uusdc")

	premium := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":      "the secret of life is 42",
			"timestamp": time.Now().Unix(),
		})
	})

	cfg := middleware.Config{
		PayTo:             payTo,
		Asset:             asset,
		Amount:            amount,
		Network:           network,
		FacilitatorURL:    facilitatorURL,
		FacilitatorAddr:   facilitatorAddr,
		MaxTimeoutSeconds: 60,
		Description:       "One call to the premium endpoint",
		MimeType:          "application/json",
		Decimals:          6,
		Symbol:            "USDC",
	}

	mux := http.NewServeMux()
	mux.Handle("/premium", middleware.Paid(cfg, premium))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	log.Printf("x402-cosmos demo server listening on %s (network=%s payTo=%s amount=%s%s)",
		addr, network, payTo, amount, asset)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env var %s is not set", k)
	}
	return v
}
