# Scheme: `exact_cosmos_authz`

Status: Draft v0.1
Targets: x402 protocol v2
Networks: Any Cosmos SDK chain with `x/authz` and `x/bank` (`cosmos:<chain-id>`)

## Motivation

x402's EVM scheme `exact_evm` relies on EIP-3009 `transferWithAuthorization`:
the payer signs a structured message off-chain, and any relayer can submit
that signature to the ERC-20 contract to pull funds. This is the property that
makes x402 "gasless" from the payer's perspective and replay-safe.

Cosmos SDK has no direct equivalent built into the bank module. Three
alternatives were considered:

1. **Pre-signed transactions.** Payer signs a full `MsgSend` tx with their
   account number and sequence; facilitator broadcasts. Fragile: a
   concurrent tx from the same account invalidates the sequence and the
   payment fails.
2. **CosmWasm vault contract.** Payer deposits funds into a contract that
   accepts ADR-036-signed pull authorizations. Closest to EIP-3009
   semantically, but limits us to CosmWasm chains (Noble — which is where
   native USDC actually lives — does not have CosmWasm).
3. **`x/authz` SendAuthorization.** Payer issues a single on-chain
   `MsgGrant` to the facilitator with a `SendAuthorization{spend_limit}`. For
   each x402 payment the payer signs an ADR-036 message authorizing one pull
   within the grant; the facilitator wraps it in `MsgExec(MsgSend)` and
   broadcasts.

This scheme uses option (3). The grant acts as a per-payer budget; the
ADR-036 signature on each payment binds it to a specific recipient, amount,
resource URL, and validity window, providing replay protection at the
protocol layer above the chain.

## Network identifiers

`network` is `cosmos:<chain-id>`, e.g. `cosmos:noble-1`. The chain-id MUST
match the on-chain `chain_id` returned by the node's `/status` RPC.

## Asset identifier

`asset` is the bank denom (e.g. `uusdc` on Noble, the IBC denom
`ibc/498A0751C798A...` for USDC.axl on Osmosis, etc.). Decimals MUST be
communicated out-of-band; this scheme works in atomic units.

## PaymentRequirements

Returned by the resource server in a 402 response.

```json
{
  "scheme": "exact_cosmos_authz",
  "network": "cosmos:noble-1",
  "maxAmountRequired": "10000",
  "asset": "uusdc",
  "payTo": "noble1abc...recipient",
  "resource": "https://api.example.com/v1/premium",
  "description": "One call to the premium endpoint",
  "mimeType": "application/json",
  "maxTimeoutSeconds": 60,
  "outputSchema": null,
  "extra": {
    "facilitator": "noble1fac...address",
    "chainId": "noble-1",
    "decimals": 6,
    "symbol": "USDC"
  }
}
```

`extra.facilitator` is the bech32 address the payer must have granted
`SendAuthorization` to. The resource server and the facilitator MUST agree
on this address; a payer with a grant to a different address will be
rejected at verify.

## PaymentPayload

Sent by the client in the `PAYMENT-SIGNATURE` HTTP header
(base64-encoded JSON, per x402 v2 transport spec).

```json
{
  "x402Version": 2,
  "scheme": "exact_cosmos_authz",
  "network": "cosmos:noble-1",
  "payload": {
    "from": "noble1pay...sender",
    "publicKey": "Ago6+...base64-secp256k1-compressed-33-bytes",
    "signature": "MEUCIQ...base64-64-byte-signature",
    "authorization": {
      "from": "noble1pay...sender",
      "to": "noble1abc...recipient",
      "denom": "uusdc",
      "amount": "10000",
      "nonce": "0x7f3a9c...32-hex-bytes",
      "validAfter": 1716700000,
      "validBefore": 1716700060,
      "resource": "https://api.example.com/v1/premium",
      "chainId": "noble-1"
    }
  }
}
```

### Authorization fields

| Field         | Type    | Notes                                             |
| ------------- | ------- | ------------------------------------------------- |
| `from`        | bech32  | Payer address. MUST equal `publicKey` bech32-ed.  |
| `to`          | bech32  | MUST equal `payTo` from PaymentRequirements.      |
| `denom`       | string  | MUST equal `asset`.                               |
| `amount`      | string  | Atomic units. MUST be ≤ `maxAmountRequired`.      |
| `nonce`       | 0x-hex  | 32 random bytes. Replay-protection key.           |
| `validAfter`  | uint64  | Unix seconds. Payment is invalid before this.     |
| `validBefore` | uint64  | Unix seconds. Payment is invalid at/after this.   |
| `resource`    | string  | Must equal `resource` from PaymentRequirements.   |
| `chainId`     | string  | MUST equal the chain-id portion of `network`.     |

## Signing (ADR-036)

The signature is an ADR-036 "arbitrary message" signature. Wallets like Keplr
and Leap support this via `signArbitrary`. The signed payload is:

1. Serialize the `authorization` object as canonical JSON: keys sorted
   lexicographically, no whitespace, UTF-8.
2. Base64-encode the result. Call this `b64data`.
3. Build the ADR-036 StdSignDoc:

```json
{
  "account_number": "0",
  "chain_id": "",
  "fee": {"amount": [], "gas": "0"},
  "memo": "",
  "msgs": [{
    "type": "sign/MsgSignData",
    "value": {
      "data": "<b64data>",
      "signer": "<from>"
    }
  }],
  "sequence": "0"
}
```

4. Sort keys lexicographically at every level, drop whitespace.
5. SHA-256 the result.
6. Sign the digest with the payer's secp256k1 key. The signature is the raw
   64-byte concatenation of `r || s` (no DER wrapping).

`publicKey` in the payload is the 33-byte compressed secp256k1 pubkey,
base64-encoded. Verifiers MUST check that bech32-encoding this pubkey using
the chain's account prefix yields exactly `authorization.from`.

## Verification (facilitator `/verify`)

The facilitator MUST reject the payment if any of the following fails:

1. `scheme == "exact_cosmos_authz"` and `network` matches a configured chain.
2. `authorization.to == payTo` (from PaymentRequirements).
3. `authorization.denom == asset`.
4. `authorization.amount <= maxAmountRequired` (string-decimal-compared).
5. `authorization.resource == resource`.
6. `authorization.chainId` matches the chain-id portion of `network`.
7. `validAfter <= now < validBefore`, with `now` the facilitator's clock.
   `validBefore - validAfter` MUST NOT exceed `maxTimeoutSeconds`.
8. `nonce` has not been observed before for this `(from, chainId)` pair.
9. ADR-036 signature verifies against `publicKey` over the reconstructed
   sign bytes.
10. Bech32(`publicKey`) == `authorization.from`.
11. Chain query: a `SendAuthorization` grant exists from `authorization.from`
    to the facilitator address, with `spend_limit` covering `amount` in
    `denom`, and not expired.

## Settlement (facilitator `/settle`)

1. Re-run all verification steps. Treat the `nonce` as **reserved** at this
   point (insert into a pending table).
2. Construct `MsgSend{from_address: authorization.from, to_address:
   authorization.to, amount: [{denom, amount}]}`.
3. Wrap in `MsgExec{grantee: facilitator_address, msgs: [MsgSend]}`.
4. Sign the tx with the facilitator's key, estimate gas, broadcast in
   sync/block mode.
5. On success: mark nonce **consumed**, return `{settled: true, txHash}`.
   On failure: release the nonce reservation (allow retry).

The facilitator pays gas. Recovering gas from the payer is out of scope for
this scheme; a future `exact_cosmos_authz_v2` may add a `feeAmount` field
deducted from the same `SendAuthorization`.

## Replay protection

Two independent layers:

- **Off-chain**: facilitator maintains `(from, chainId, nonce)` set.
  Duplicate nonce → reject.
- **On-chain**: the `SendAuthorization`'s `spend_limit` decrements with
  every `MsgExec`. Once exhausted, no further pulls succeed regardless of
  off-chain state.

The off-chain layer is what prevents the facilitator from double-settling
the same authorization (e.g. across restarts); persist the nonce set
durably.

## Error codes

Per x402 spec, returned in `PAYMENT-RESPONSE` header on settle failure:

| `errorReason`                | Meaning                                       |
| ---------------------------- | --------------------------------------------- |
| `invalid_signature`          | ADR-036 verification failed.                  |
| `invalid_authorization`      | Field mismatch with PaymentRequirements.      |
| `nonce_already_used`         | Replay detected.                              |
| `expired_authorization`      | `now >= validBefore` or `now < validAfter`.   |
| `insufficient_grant`         | No grant, or `spend_limit` < `amount`.        |
| `insufficient_funds`         | Payer's bank balance < `amount`.              |
| `broadcast_failed`           | Tx rejected by node or timed out.             |
| `unexpected_settle_error`    | Anything else.                                |

## Future work

- `exact_cosmos_cw` scheme using a CosmWasm vault for fully stateless,
  contract-enforced replay protection on CosmWasm chains.
- IBC-aware variant where `payTo` is on a different chain than the grant.
- `upto` scheme variant (metered/streaming payments).
