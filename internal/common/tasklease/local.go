package tasklease

import (
	"context"
	"errors"
	"sync"
)

// LocalPool is the single-process counterpart used by Lite batch/finalize
// locks. Its zero value is ready. Distributed work must use persistent claims.
type LocalPool struct {
	mu     sync.Mutex
	active map[string]int
}

func (p *LocalPool) TryClaim(ctx context.Context, key string, limit int) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if key == "" || limit < 1 {
		return nil, errors.New("invalid local background claim")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = map[string]int{}
	}
	if p.active[key] >= limit {
		return nil, ErrBusy
	}
	p.active[key]++
	var once sync.Once
	return Funcs{ReleaseFunc: func(context.Context) error {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.active[key]--
			if p.active[key] == 0 {
				delete(p.active, key)
			}
		})
		return nil
	}}, nil
}
