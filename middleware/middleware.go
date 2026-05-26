// Package middleware provides a net/http middleware that gates a handler
// behind an x402-cosmos payment. On the first request the middleware
// returns 402 with PAYMENT-REQUIRED; if the client retries with a valid
// PAYMENT-SIGNATURE header, the middleware forwards the payment to the
// facilitator's /settle endpoint and, on success, calls the inner handler.
package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	x402cosmos "github.com/yourorg/x402-cosmos/facilitator"
)

// Config holds the payment parameters this middleware enforces.
type Config struct {
	PayTo             string // bech32 recipient
	Asset             string // bank denom, e.g. "uusdc"
	Amount            string // atomic units, decimal string
	Network           string // "cosmos:<chain-id>", e.g. "cosmos:grand-1"
	FacilitatorURL    string // base URL of the facilitator HTTP API
	FacilitatorAddr   string // facilitator's bech32 (grantee for SendAuthorization)
	MaxTimeoutSeconds uint64 // default 60 if zero
	Description       string
	MimeType          string // optional; defaults from inner handler
	Decimals          uint8  // optional; informational only
	Symbol            string // optional; informational only

	// HTTPClient lets callers inject a custom client (timeouts, transport).
	// If nil, a sensible default with a 30s timeout is used.
	HTTPClient *http.Client
}

// Paid wraps `next` so that every request must be accompanied by a valid
// x402 payment. The middleware itself decides the canonical resource URL
// for each request (scheme://host/path with no query).
func Paid(cfg Config, next http.Handler) http.Handler {
	if cfg.MaxTimeoutSeconds == 0 {
		cfg.MaxTimeoutSeconds = 60
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requirements := buildRequirements(cfg, r)

		sigHeader := r.Header.Get("PAYMENT-SIGNATURE")
		if sigHeader == "" {
			writeRequired(w, requirements)
			return
		}

		payload, err := decodePayload(sigHeader)
		if err != nil {
			writeFailure(w, requirements, x402cosmos.ErrInvalidAuthorization,
				fmt.Sprintf("decode payload: %v", err))
			return
		}

		resp, err := callSettle(r.Context(), cfg, payload, requirements)
		if err != nil {
			writeFailure(w, requirements, x402cosmos.ErrUnexpected,
				fmt.Sprintf("facilitator call failed: %v", err))
			return
		}
		if !resp.Success {
			writeFailure(w, requirements, resp.ErrorReason, "")
			return
		}

		// Payment settled. Expose the receipt to the inner handler via the
		// response header and call through.
		setResponseHeader(w, resp)
		next.ServeHTTP(w, r)
	})
}

func buildRequirements(cfg Config, r *http.Request) x402cosmos.PaymentRequirements {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	resource := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.Path)

	chainID := cfg.Network
	if len(chainID) > 7 && chainID[:7] == "cosmos:" {
		chainID = chainID[7:]
	}

	return x402cosmos.PaymentRequirements{
		Scheme:            x402cosmos.Scheme,
		Network:           cfg.Network,
		MaxAmountRequired: cfg.Amount,
		Asset:             cfg.Asset,
		PayTo:             cfg.PayTo,
		Resource:          resource,
		Description:       cfg.Description,
		MimeType:          cfg.MimeType,
		MaxTimeoutSeconds: cfg.MaxTimeoutSeconds,
		Extra: x402cosmos.RequirementsExtra{
			Facilitator: cfg.FacilitatorAddr,
			ChainID:     chainID,
			Decimals:    cfg.Decimals,
			Symbol:      cfg.Symbol,
		},
	}
}

func writeRequired(w http.ResponseWriter, req x402cosmos.PaymentRequirements) {
	b, _ := json.Marshal(req)
	w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(b))
	w.Header().Set("Access-Control-Expose-Headers", "PAYMENT-REQUIRED, PAYMENT-RESPONSE")
	w.WriteHeader(http.StatusPaymentRequired)
}

func writeFailure(w http.ResponseWriter, req x402cosmos.PaymentRequirements, code, _ string) {
	// PAYMENT-REQUIRED still shows the client what they need to pay.
	reqBytes, _ := json.Marshal(req)
	w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(reqBytes))
	resp := x402cosmos.SettleResponse{Success: false, ErrorReason: code, Network: req.Network}
	respBytes, _ := json.Marshal(resp)
	w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(respBytes))
	w.Header().Set("Access-Control-Expose-Headers", "PAYMENT-REQUIRED, PAYMENT-RESPONSE")
	w.WriteHeader(http.StatusPaymentRequired)
}

func setResponseHeader(w http.ResponseWriter, resp x402cosmos.SettleResponse) {
	b, _ := json.Marshal(resp)
	w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(b))
	w.Header().Set("Access-Control-Expose-Headers", "PAYMENT-RESPONSE")
}

func decodePayload(header string) (x402cosmos.PaymentPayload, error) {
	var pp x402cosmos.PaymentPayload
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return pp, fmt.Errorf("base64: %w", err)
	}
	if err := json.Unmarshal(raw, &pp); err != nil {
		return pp, fmt.Errorf("json: %w", err)
	}
	if pp.Scheme != x402cosmos.Scheme {
		return pp, errors.New("scheme mismatch")
	}
	return pp, nil
}

func callSettle(ctx context.Context, cfg Config, payload x402cosmos.PaymentPayload, requirements x402cosmos.PaymentRequirements) (x402cosmos.SettleResponse, error) {
	body, err := json.Marshal(x402cosmos.SettleRequest{
		X402Version:         2,
		PaymentPayload:      payload,
		PaymentRequirements: requirements,
	})
	if err != nil {
		return x402cosmos.SettleResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.FacilitatorURL+"/settle", bytes.NewReader(body))
	if err != nil {
		return x402cosmos.SettleResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return x402cosmos.SettleResponse{}, err
	}
	defer httpResp.Body.Close()

	var resp x402cosmos.SettleResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return x402cosmos.SettleResponse{}, fmt.Errorf("decode settle response: %w", err)
	}
	return resp, nil
}
