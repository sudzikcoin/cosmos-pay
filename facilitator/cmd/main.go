// Command facilitator runs the x402-cosmos facilitator HTTP server,
// exposing /verify and /settle endpoints compatible with the x402 v2
// facilitator API.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	x402cosmos "github.com/yourorg/x402-cosmos/facilitator"
)

func main() {
	// Minimal config: facilitator key, RPC endpoints per chain. In a real
	// build these come from env/flags. Stubbed here.
	addr := getenv("X402_FACILITATOR_ADDR", ":8402")

	nobleClient, err := x402cosmos.NewNobleClient(context.Background())
	if err != nil {
		log.Fatalf("init noble client: %v", err)
	}
	log.Printf("facilitator addr=%s chain-id=%s", nobleClient.Address(), nobleClient.ChainID())
	chains := map[string]x402cosmos.ChainClient{
		"cosmos:" + nobleClient.ChainID(): nobleClient,
	}
	store := x402cosmos.NewMemoryNonceStore()
	verifier := x402cosmos.NewVerifier(chains, store)
	settler := x402cosmos.NewSettler(verifier)

	// Background GC for stale nonce reservations.
	go func() {
		t := time.NewTicker(30 * time.Second)
		for range t.C {
			n := store.GC(time.Now())
			if n > 0 {
				log.Printf("nonce gc: cleared %d expired reservations", n)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/verify", verifyHandler(verifier))
	mux.HandleFunc("/settle", settleHandler(settler))
	mux.HandleFunc("/supported", supportedHandler(chains))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("x402-cosmos facilitator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func verifyHandler(v *x402cosmos.Verifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req x402cosmos.VerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, x402cosmos.VerifyResponse{IsValid: false, InvalidReason: "bad request: " + err.Error()})
			return
		}
		payer, err := v.Verify(r.Context(), req)
		if err != nil {
			writeJSON(w, 200, x402cosmos.VerifyResponse{IsValid: false, InvalidReason: x402cosmos.ErrorCode(err)})
			return
		}
		writeJSON(w, 200, x402cosmos.VerifyResponse{IsValid: true, Payer: payer})
	}
}

func settleHandler(s *x402cosmos.Settler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req x402cosmos.SettleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, x402cosmos.SettleResponse{Success: false, ErrorReason: "bad_request"})
			return
		}
		resp := s.Settle(r.Context(), req)
		writeJSON(w, 200, resp)
	}
}

// supportedHandler returns the list of (scheme, network) pairs this
// facilitator can handle. x402 clients hit this to pick a compatible
// facilitator.
func supportedHandler(chains map[string]x402cosmos.ChainClient) http.HandlerFunc {
	type pair struct {
		Scheme  string `json:"scheme"`
		Network string `json:"network"`
	}
	type resp struct {
		Kinds []pair `json:"kinds"`
	}
	out := resp{}
	for network := range chains {
		out.Kinds = append(out.Kinds, pair{Scheme: x402cosmos.Scheme, Network: network})
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

