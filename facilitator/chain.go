package x402cosmos

import (
	"context"
	"time"
)

// ChainClient is the abstraction the facilitator uses for all on-chain
// interactions. Concrete implementations wrap a Cosmos SDK gRPC client.
// Mocked in tests.
type ChainClient interface {
	// ChainID returns the chain-id this client is bound to.
	ChainID() string

	// AccountPrefix returns the bech32 account prefix for this chain
	// (e.g. "noble", "cosmos", "osmo").
	AccountPrefix() string

	// QuerySendAuthorization returns the spend limit (in `denom`) granted
	// from `granter` to `grantee`, or (nil, ErrNoGrant) if no matching
	// SendAuthorization exists. `expiration` is zero if the grant has no
	// expiry.
	QuerySendAuthorization(ctx context.Context, granter, grantee, denom string) (spendLimit string, expiration time.Time, err error)

	// QueryBalance returns the payer's spendable balance of `denom`.
	QueryBalance(ctx context.Context, addr, denom string) (string, error)

	// BroadcastAuthzSend builds, signs, and broadcasts a
	// MsgExec{MsgSend{from,to,amount,denom}} from the facilitator
	// (grantee). Returns the tx hash on inclusion, or an error if the tx
	// is rejected or times out.
	BroadcastAuthzSend(ctx context.Context, from, to, denom, amount string) (txHash string, err error)
}

// ErrNoGrant is returned by QuerySendAuthorization when no matching grant
// exists.
type chainError string

func (e chainError) Error() string { return string(e) }

const (
	ErrNoGrant  chainError = "no SendAuthorization grant found"
	ErrChainRPC chainError = "chain rpc error"
)
