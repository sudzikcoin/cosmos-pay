package x402cosmos

import (
	"context"
	"time"
)

// Settler handles the /settle endpoint. It composes a Verifier and adds
// nonce reservation + on-chain broadcast.
type Settler struct {
	v *Verifier
}

func NewSettler(v *Verifier) *Settler { return &Settler{v: v} }

// Settle re-runs verification, reserves the nonce, broadcasts the
// MsgExec(MsgSend), and commits the nonce on success. On any failure
// after reservation the nonce is released so the payer can retry.
func (s *Settler) Settle(ctx context.Context, req SettleRequest) SettleResponse {
	pp := req.PaymentPayload
	a := pp.Payload.Authorization

	payer, err := s.v.Verify(ctx, req)
	if err != nil {
		return SettleResponse{
			Success:     false,
			ErrorReason: ErrorCode(err),
			Network:     pp.Network,
		}
	}

	// Reserve nonce before any side effect on chain.
	validBefore := time.Unix(int64(a.ValidBefore), 0)
	if err := s.v.nonces.Reserve(ctx, a.From, a.ChainID, a.Nonce, validBefore); err != nil {
		code := ErrUnexpected
		if err == ErrNonceUsed {
			code = ErrNonceAlreadyUsed
		}
		return SettleResponse{
			Success:     false,
			ErrorReason: code,
			Network:     pp.Network,
			Payer:       payer,
		}
	}

	client := s.v.chains[pp.Network]
	txHash, err := client.BroadcastAuthzSend(ctx, a.From, a.To, a.Denom, a.Amount)
	if err != nil {
		_ = s.v.nonces.Release(ctx, a.From, a.ChainID, a.Nonce)
		return SettleResponse{
			Success:     false,
			ErrorReason: ErrBroadcastFailed,
			Network:     pp.Network,
			Payer:       payer,
		}
	}

	if err := s.v.nonces.Commit(ctx, a.From, a.ChainID, a.Nonce, txHash); err != nil {
		// Tx was included but we couldn't update local state. This is a
		// non-recoverable accounting issue; return success since the
		// payment did happen, but log loudly upstream.
		// (Production: alert here.)
	}

	return SettleResponse{
		Success:     true,
		Transaction: txHash,
		Network:     pp.Network,
		Payer:       payer,
	}
}
