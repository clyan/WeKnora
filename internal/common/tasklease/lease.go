// Package tasklease maintains claimed background work without deciding what a
// document batch, knowledge topic or model request means. Storage adapters own
// atomic admission and fencing; this package owns renewal and orderly release.
package tasklease

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrBusy = errors.New("background work is already claimed or at capacity")
	ErrLost = errors.New("background work lease lost")
)

// Lease operations must check ownership atomically. A failed renewal must not
// create a new claim, and release must never remove a replacement owner's claim.
type Lease interface {
	Renew(context.Context) error
	Release(context.Context) error
}

// Funcs adapts transactional repository claims to the shared lifecycle. Topics
// release ownership in their checkpoint transaction, so ReleaseFunc may be nil.
type Funcs struct {
	RenewFunc   func(context.Context) error
	ReleaseFunc func(context.Context) error
}

func (f Funcs) Renew(ctx context.Context) error {
	if f.RenewFunc == nil {
		return nil
	}
	return f.RenewFunc(ctx)
}
func (f Funcs) Release(ctx context.Context) error {
	if f.ReleaseFunc == nil {
		return nil
	}
	return f.ReleaseFunc(ctx)
}

type Handle struct {
	ctx       context.Context
	cancel    context.CancelCauseFunc
	lease     Lease
	done      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

// Claim invokes one nonblocking, storage-specific admission attempt, then
// maintains ownership. ErrBusy means requeue; it does not hold a worker waiting.
// interval==0 is for local locks or claims whose lifetime is bounded elsewhere.
func Claim(ctx context.Context, acquire func(context.Context) (Lease, error), interval time.Duration) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if acquire == nil || interval < 0 {
		return nil, errors.New("invalid background lease configuration")
	}
	lease, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, errors.New("claim returned no background lease")
	}
	workCtx, cancel := context.WithCancelCause(ctx)
	h := &Handle{ctx: workCtx, cancel: cancel, lease: lease, done: make(chan struct{})}
	if interval == 0 {
		close(h.done)
	} else {
		go h.renew(interval)
	}
	return h, nil
}

func (h *Handle) Context() context.Context { return h.ctx }

func (h *Handle) renew(interval time.Duration) {
	defer close(h.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			if h.ctx.Err() != nil {
				return
			}
			if err := h.lease.Renew(h.ctx); err != nil {
				if h.ctx.Err() == nil {
					h.cancel(errors.Join(ErrLost, err))
				}
				return
			}
		}
	}
}

// Stop joins renewal before a caller commits or releases its claim. Merely
// canceling a goroutine can race with a renewal already inside the backend.
func (h *Handle) Stop() {
	h.stopOnce.Do(func() { h.cancel(context.Canceled); <-h.done })
}

// Close is safe after cancellation and safe to call repeatedly/concurrently.
func (h *Handle) Close() error {
	h.closeOnce.Do(func() {
		h.Stop()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), 5*time.Second)
		defer cancel()
		h.closeErr = h.lease.Release(ctx)
		if cause := context.Cause(h.ctx); errors.Is(cause, ErrLost) {
			h.closeErr = errors.Join(cause, h.closeErr)
		}
	})
	return h.closeErr
}
