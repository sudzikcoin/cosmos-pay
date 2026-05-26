# CLAUDE.md — x402-cosmos project context

This file is loaded automatically by Claude Code. It contains the
always-relevant architectural context for this repo. Read fully on first
load; consult `TASK.md` for the current job.

## What this repo is

x402-cosmos is an extension of the [x402 payment
protocol](https://github.com/x402-foundation/x402) to Cosmos SDK chains.
x402 uses the HTTP 402 status code to negotiate stablecoin payments. The
reference implementation supports EVM and Solana. This repo adds **Noble**
(native USDC on Cosmos) as the first target chain, with the design
extensible to any Cosmos SDK chain.

## Architectural law: the spec is canonical

`specs/scheme_exact_cosmos_authz.md` is the source of truth. All code must
conform to it byte-for-byte on the wire. If you discover a discrepancy
between code and spec, **update both deliberately** — do not silently
"fix" the code to match the spec or vice versa. Wire-format changes
require updating the version in `types.go` (`Scheme` constant + payload
`x402Version` field).

## Core design choice

Cosmos has no native equivalent of EIP-3009 `transferWithAuthorization`.
This repo uses `x/authz` `SendAuthorization` instead:

1. Payer issues a one-time on-chain `MsgGrant` to the facilitator with a
   `SendAuthorization{spend_limit}`. This caps total spend.
2. For each x402 payment, payer signs an ADR-036 message authorizing one
   pull within the grant (binding to recipient, amount, resource URL,
   validity window, nonce).
3. Facilitator wraps the signed authorization in `MsgExec(MsgSend)` and
   broadcasts. Facilitator pays gas (in `uusdc` on Noble).
4. Replay protection: off-chain nonce set + on-chain `spend_limit`
   decrement.

Rejected alternatives and why (so we don't relitigate):

- **Pre-signed `MsgSend`** — sequence-number collisions if payer sends
  any other tx. Fragile.
- **CosmWasm vault contract** — would be closest to EIP-3009 semantics,
  but Noble (where native USDC actually lives) has no CosmWasm. Kept as a
  future `exact_cosmos_cw` scheme for Osmosis/Neutron.

## Repo layout

```
specs/
  scheme_exact_cosmos_authz.md   Protocol spec — CANONICAL
facilitator/
  types.go                       Shared structs matching the spec
  adr036.go                      ADR-036 sign bytes + signature verification
  verify.go                      11-step verification (spec §Verification)
  settle.go                      Settlement flow with nonce lifecycle
  store.go                       NonceStore interface + in-memory impl
  chain.go                       ChainClient interface
  chain_noble.go                 Noble gRPC implementation
  cmd/main.go                    HTTP server: /verify, /settle, /supported
middleware/
  middleware.go                  Drop-in Go middleware for resource servers
examples/
  server/main.go                 Demo paid API
  client/main.go                 Go demo client
tools/
  grant/main.go                  One-time MsgGrant helper
```

## Target network

**Noble testnet:**
- Chain ID: `grand-1`
- Bech32 prefix: `noble`
- Gas token: `uusdc` (USDC IS the gas token on Noble — no separate fee token)
- RPC: `https://rpc.testnet.noble.xyz`
- gRPC: `noble-testnet-grpc.polkachu.com:21590` (plaintext on this port)
- Faucet: <https://faucet.circle.com> → select "Noble Testnet"
- Min gas price: `0.1uusdc`
- SLIP44: 118

## Critical gotchas

1. **ADR-036 sign-bytes determinism.** `sdk.SortJSON` handles canonical
   JSON — use it, don't roll your own. If signatures fail to verify,
   FIRST log the raw sortedDoc bytes on both sides and diff them.
2. **Pubkey → address derivation.** `secp256k1.PubKey.Address()` returns
   the 20-byte address; bech32-encode with the chain's prefix.
3. **`MsgExec` gas estimation.** Multiply by 1.5x as a safety margin.
4. **`SendAuthorization`** lives in `x/bank/types`, not `x/authz/types`.
5. **Validity window** is `validAfter <= now < validBefore`
   (validBefore is exclusive).
