# TASK.md — bring x402-cosmos to end-to-end on Noble testnet

## Goal

A working PoC of the x402 protocol on Noble testnet (`grand-1`). Three
processes — facilitator, server, client — interact such that a payment
flows from a payer's wallet to a recipient's wallet over real on-chain
transactions, mediated by a real `SendAuthorization` grant.

Not a mock. Real chain. Real (testnet) USDC moving.

## What is already in the repo

- `CLAUDE.md` — architecture, conventions, gotchas.
- `specs/scheme_exact_cosmos_authz.md` — canonical protocol spec.
- `facilitator/types.go` — Go structs matching the spec exactly.
- `facilitator/adr036.go` — ADR-036 sign-bytes + signature verification.
- `facilitator/verify.go` — 11-step verification per spec.
- `facilitator/settle.go` — settle flow with nonce reservation.
- `facilitator/store.go` — `NonceStore` interface, in-memory impl.
- `facilitator/chain.go` — `ChainClient` interface.
- `facilitator/cmd/main.go` — HTTP server skeleton (panics at
  `mustNobleClient()` — replace).
- `facilitator/adr036_test.go` — golden wire-format test.

## What to build

1. **`facilitator/chain_noble.go`** — NobleClient implementing ChainClient
   via gRPC (QuerySendAuthorization, QueryBalance, BroadcastAuthzSend).
2. **`middleware/middleware.go`** — `net/http` middleware: 402 with
   PAYMENT-REQUIRED → /settle round-trip on signed retry.
3. **`examples/server/main.go`** — demo paid endpoint.
4. **`tools/grant/main.go`** — one-shot `MsgGrant` CLI from payer mnemonic.
5. **`examples/client/main.go`** — Go demo client that signs ADR-036 and
   retries.
6. **README.md** — full run instructions (3 terminals, env vars).

## Acceptance criteria

1. `go build ./...` exits 0.
2. `go test ./...` exits 0 (ADR-036 golden test stays green).
3. End-to-end on Noble testnet `grand-1`:
   - `tools/grant` issues `SendAuthorization` (tx visible on Mintscan).
   - Server returns 402 with parseable `PAYMENT-REQUIRED` on first call.
   - Signed retry returns 200 + real tx hash in `PAYMENT-RESPONSE`.
4. Replay rejected → `nonce_already_used`.
5. Missing grant rejected → `insufficient_grant`.
6. Expired authz rejected → `expired_authorization`.

## How to test (human steps)

```bash
# 1. Fund payer + facilitator at https://faucet.circle.com (Noble testnet).
# 2. One-time grant from payer to facilitator:
export PAYER_MNEMONIC="..."
go run ./tools/grant \
    --mnemonic "$PAYER_MNEMONIC" \
    --grantee <FACILITATOR_BECH32> \
    --spend-limit 1000000uusdc \
    --expiration 24h
# 3. Terminal 1: facilitator
export X402_FACILITATOR_MNEMONIC="..."
go run ./facilitator/cmd
# 4. Terminal 2: server
export X402_PAY_TO=<RECIPIENT_BECH32>
export X402_AMOUNT=10000
export X402_FACILITATOR_URL=http://localhost:8402
export X402_FACILITATOR_GRANTEE=<FACILITATOR_BECH32>
go run ./examples/server
# 5. Terminal 3: client
export X402_PAYER_MNEMONIC="..."
export X402_SERVER_URL=http://localhost:8080
go run ./examples/client
```
