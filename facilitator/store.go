package x402cosmos

import (
	"context"
	"errors"
	"sync"
	"time"
)

// NonceStore tracks observed (payer, chain, nonce) triples to prevent
// replay. Implementations MUST be durable in production; the in-memory
// store here is for local development only.
//
// Lifecycle:
//   - Reserve: called before broadcasting a settle tx. Atomically inserts
//     the nonce in "pending" state. Returns ErrNonceUsed if already present.
//   - Commit: called after the tx is successfully included on-chain.
//     Promotes the nonce to "consumed".
//   - Release: called if broadcast/inclusion fails. Removes the reservation
//     so the payer can retry with the same authorization.
type NonceStore interface {
	Reserve(ctx context.Context, payer, chainID, nonce string, validBefore time.Time) error
	Commit(ctx context.Context, payer, chainID, nonce, txHash string) error
	Release(ctx context.Context, payer, chainID, nonce string) error
	// Exists is a read-only check used by /verify (without reserving).
	Exists(ctx context.Context, payer, chainID, nonce string) (bool, error)
}

// ErrNonceUsed is returned by Reserve when the nonce has already been
// seen (either pending or committed) for the same payer+chain.
var ErrNonceUsed = errors.New("nonce already used")

type nonceState struct {
	committed   bool
	txHash      string
	validBefore time.Time
}

// MemoryNonceStore is a sync.Map-backed in-memory store. Suitable for the
// PoC and local tests. Loses state on restart — DO NOT use in production.
type MemoryNonceStore struct {
	mu sync.Mutex
	m  map[string]*nonceState
}

func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{m: make(map[string]*nonceState)}
}

func nonceKey(payer, chainID, nonce string) string {
	return payer + "|" + chainID + "|" + nonce
}

func (s *MemoryNonceStore) Reserve(_ context.Context, payer, chainID, nonce string, validBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := nonceKey(payer, chainID, nonce)
	if _, ok := s.m[k]; ok {
		return ErrNonceUsed
	}
	s.m[k] = &nonceState{validBefore: validBefore}
	return nil
}

func (s *MemoryNonceStore) Commit(_ context.Context, payer, chainID, nonce, txHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := nonceKey(payer, chainID, nonce)
	st, ok := s.m[k]
	if !ok {
		return errors.New("commit without reserve")
	}
	st.committed = true
	st.txHash = txHash
	return nil
}

func (s *MemoryNonceStore) Release(_ context.Context, payer, chainID, nonce string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := nonceKey(payer, chainID, nonce)
	st, ok := s.m[k]
	if !ok {
		return nil // idempotent
	}
	if st.committed {
		return errors.New("cannot release committed nonce")
	}
	delete(s.m, k)
	return nil
}

func (s *MemoryNonceStore) Exists(_ context.Context, payer, chainID, nonce string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[nonceKey(payer, chainID, nonce)]
	return ok, nil
}

// GC removes expired uncommitted reservations. Call periodically.
func (s *MemoryNonceStore) GC(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, st := range s.m {
		if !st.committed && now.After(st.validBefore) {
			delete(s.m, k)
			n++
		}
	}
	return n
}
