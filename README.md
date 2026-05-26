# cosmos-pay

> **Status:** End-to-end verified on **Noble testnet `grand-1`** —
> [example settlement tx on Mintscan](https://www.mintscan.io/noble-testnet/txs/26D6C45FDE52D5A4F0EDEB9ED37ACCD9A6C98DBFAA24B5AEDE9F0CB25551C7FC).

x402 payment protocol for Cosmos SDK chains. Brings HTTP-native stablecoin
payments (USDC, etc.) to Noble, Osmosis, Cosmos Hub, and any other Cosmos
SDK chain — fully compatible with the x402 v2 spec.

## What this is

[x402](https://github.com/x402-foundation/x402) uses HTTP 402 to negotiate
stablecoin payments between clients and servers. The reference
implementation supports EVM and Solana. This repo adds **Cosmos** support,
targeting **Noble** (native USDC) first.

## How

x402 is chain-agnostic via two extension points: `scheme` and `network`.
We introduce:

- **scheme `exact_cosmos_authz`** — payer issues a one-time `MsgGrant`
  with a `SendAuthorization` to the facilitator, capping total spend.
  Each x402 payment is an ADR-036-signed authorization the facilitator
  unwraps into a `MsgExec(MsgSend)`. No smart contract needed, works on
  any Cosmos SDK chain including Noble.
- **network `cosmos:<chain-id>`** — e.g. `cosmos:grand-1` (Noble
  testnet), `cosmos:noble-1` (mainnet), `cosmos:osmosis-1`,
  `cosmos:cosmoshub-4`.

Spec: [`specs/scheme_exact_cosmos_authz.md`](specs/scheme_exact_cosmos_authz.md).

## Repo layout

```
specs/                            protocol spec (canonical)
facilitator/                      /verify + /settle service
  cmd/main.go                       HTTP server entry point
  chain_noble.go                    Noble gRPC client
  adr036.go                         ADR-036 sign-bytes + verification
  verify.go, settle.go              11-step verification + settle flow
  store.go                          NonceStore (in-memory PoC impl)
  types.go, chain.go                wire types + ChainClient interface
middleware/middleware.go          net/http middleware for resource servers
examples/
  server/main.go                    demo paid endpoint
  client/main.go                    demo client (signs ADR-036, retries)
tools/
  grant/main.go                     one-time MsgGrant CLI for payers
  keygen/main.go                    fresh BIP-39 mnemonic + address generator
```

## Quick start

A fresh checkout to working end-to-end payment in ~5 minutes:

```bash
# 1. Clone + install deps
git clone https://github.com/suverse/cosmos-pay.git
cd cosmos-pay
go mod tidy

# 2. Generate two testnet mnemonics + addresses
go run ./tools/keygen
# Save both mnemonics + addresses — you'll need them below.

# 3. Fund both addresses with testnet USDC
# Go to https://faucet.circle.com, select "Noble Testnet", paste each address.
# Payer needs ~5 USDC; facilitator needs ~1 USDC (gas, paid in uusdc).

# 4. Issue the one-time SendAuthorization (payer → facilitator)
export PAYER_MNEMONIC="<payer 24 words>"
go run ./tools/grant \
    --grantee noble1<FACILITATOR_ADDR> \
    --spend-limit 1000000uusdc \
    --expiration 24h

# 5. Terminal A — facilitator
export X402_FACILITATOR_MNEMONIC="<facilitator 24 words>"
go run ./facilitator/cmd
# logs: facilitator addr=noble1... chain-id=grand-1

# 6. Terminal B — demo server
export X402_PAY_TO=noble1<RECIPIENT_ADDR>          # can equal payer
export X402_AMOUNT=10000                            # 0.01 USDC
export X402_FACILITATOR_GRANTEE=noble1<FACILITATOR_ADDR>
go run ./examples/server

# 7. Terminal C — client
export X402_PAYER_MNEMONIC="$PAYER_MNEMONIC"
go run ./examples/client
# logs: PAYMENT SETTLED tx=<hash>  +  prints the response body
```

The client's tx hash is a real on-chain settlement — open
`https://www.mintscan.io/noble-testnet/txs/<hash>` to verify it contains
a `MsgExec` wrapping a `MsgSend` from payer → recipient.

## Tools

### `tools/keygen`

Generates two fresh BIP-39 mnemonics + their Noble addresses. Testnet-only
— never use the output for mainnet funds. Mnemonics are printed to stdout;
nothing is written to disk.

```bash
go run ./tools/keygen
```

### `tools/grant`

Sends a single `MsgGrant{SendAuthorization}` from the payer to the
facilitator. Run once per payer-facilitator pair; the spend limit decrements
on every payment and you can refresh it by rerunning.

```bash
go run ./tools/grant \
    --mnemonic "$PAYER_MNEMONIC" \
    --grantee noble1<FACILITATOR_ADDR> \
    --spend-limit 1000000uusdc \
    --expiration 24h
```

Use `--help` for the full flag list; defaults target Noble testnet.

## Environment variables

| Var                          | Default                                          | Used by              |
| ---------------------------- | ------------------------------------------------ | -------------------- |
| `X402_FACILITATOR_MNEMONIC`  | (required)                                       | facilitator          |
| `X402_FACILITATOR_ADDR`      | `:8402`                                          | facilitator          |
| `X402_FACILITATOR_URL`       | `http://localhost:8402`                          | server, middleware   |
| `X402_FACILITATOR_GRANTEE`   | (required)                                       | server (grantee bech32) |
| `X402_PAY_TO`                | (required)                                       | server (recipient)   |
| `X402_AMOUNT`                | `10000`                                          | server (atomic units) |
| `X402_NETWORK`               | `cosmos:grand-1`                                 | server               |
| `X402_ASSET`                 | `uusdc`                                          | server               |
| `X402_SERVER_ADDR`           | `:8080`                                          | server               |
| `X402_SERVER_URL`            | `http://localhost:8080/premium`                  | client               |
| `X402_PAYER_MNEMONIC`        | (required)                                       | client, grant CLI    |
| `X402_NOBLE_RPC`             | `https://noble-testnet-rpc.polkachu.com`         | facilitator, grant   |
| `X402_NOBLE_GRPC`            | `noble-testnet-grpc.polkachu.com:21590`          | facilitator, grant   |
| `X402_NOBLE_GRPC_TLS`        | (unset; plaintext)                               | facilitator, grant   |
| `X402_NOBLE_CHAIN_ID`        | `grand-1`                                        | facilitator, grant   |
| `X402_NOBLE_PREFIX`          | `noble`                                          | facilitator, grant   |
| `X402_NOBLE_DENOM`           | `uusdc`                                          | facilitator          |
| `X402_REUSE_NONCE=1`         |                                                  | client (replay test) |
| `X402_EXPIRED=1`             |                                                  | client (expiry test) |

> The defaults target Noble testnet (`grand-1`). The official Noble RPC
> (`rpc.testnet.noble.xyz`) was running ~48h behind block tip in our
> testing, so we default to polkachu's mirror.

## Tests

```bash
go test ./...                                                 # all tests
go test ./facilitator -run TestCanonicalAuthorizationJSON -v  # wire-format golden
go test ./facilitator -run TestSignAndVerifyRoundTrip -v      # client↔server agreement
```

The canonical-JSON golden test pins the exact bytes the payer signs over.
If it breaks, signature verification breaks everywhere — only change it by
deliberately bumping `x402Version`.

## Negative-path verification

The demo client supports two flags for testing error paths:

```bash
X402_REUSE_NONCE=1 go run ./examples/client    # first call settles
X402_REUSE_NONCE=1 go run ./examples/client    # second: nonce_already_used
X402_EXPIRED=1 go run ./examples/client        # expired_authorization
```

For `insufficient_grant`, run a second server pointing at a freshly-generated
(but never-granted-to) facilitator address:

```bash
X402_SERVER_ADDR=:8090 \
X402_FACILITATOR_GRANTEE=noble1<FRESH_ADDR_FROM_KEYGEN> \
go run ./examples/server &
X402_SERVER_URL=http://localhost:8090/premium go run ./examples/client
# → PAYMENT FAILED errorReason="insufficient_grant"
```

## Status

PoC. Targets Noble testnet first; not audited; not for production. The
in-memory `MemoryNonceStore` loses replay-protection state on facilitator
restart — production needs a durable store (Postgres/Redis). For
production deployment, also harden the `chain_noble.go::QuerySendAuthorization`
error mapping and add structured logging on `/settle`.
