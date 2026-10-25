package tasklease

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func leaseRedis(t *testing.T) *redis.Client {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestClaimStopsAndJoinsRenewalBeforeRelease(t *testing.T) {
	entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releases atomic.Int32
	h, err := Claim(context.Background(), func(context.Context) (Lease, error) {
		return Funcs{RenewFunc: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-finish
			return ctx.Err()
		}, ReleaseFunc: func(ctx context.Context) error {
			releases.Add(1)
			return ctx.Err()
		}}, nil
	}, time.Millisecond)
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("renewal was not canceled")
	}
	require.Zero(t, releases.Load(), "release must wait for the in-flight renewal")
	close(finish)
	require.NoError(t, <-done)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.Close() }()
	}
	wg.Wait()
	require.EqualValues(t, 1, releases.Load())
}

func TestRenewalLossCancelsWorkAndIsReturnedAtClose(t *testing.T) {
	backendErr := errors.New("ownership was replaced")
	h, err := Claim(context.Background(), func(context.Context) (Lease, error) {
		return Funcs{RenewFunc: func(context.Context) error { return backendErr }}, nil
	}, time.Millisecond)
	require.NoError(t, err)
	select {
	case <-h.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("work did not stop after losing its lease")
	}
	require.ErrorIs(t, context.Cause(h.Context()), ErrLost)
	require.ErrorIs(t, h.Close(), backendErr)
	require.ErrorIs(t, h.Close(), ErrLost)
}

func TestLocalAndRedisClaimsRespectIndependentCapacity(t *testing.T) {
	client := leaseRedis(t)
	var local LocalPool
	for _, store := range []string{"local", "redis"} {
		t.Run(store, func(t *testing.T) {
			acquire := func(key string) (Lease, error) {
				if store == "local" {
					return local.TryClaim(context.Background(), key, 3)
				}
				return TrySlots(context.Background(), client, key, 3, time.Minute)
			}
			leases := make(chan Lease, 32)
			var wg sync.WaitGroup
			var unexpected atomic.Int32
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					l, err := acquire("wiki:kb-a")
					if err == nil {
						leases <- l
					} else if !errors.Is(err, ErrBusy) {
						unexpected.Add(1)
					}
				}()
			}
			wg.Wait()
			close(leases)
			require.Zero(t, unexpected.Load())
			require.Len(t, leases, 3, "more workers cannot bypass the KB cap")
			other, err := acquire("wiki:kb-b")
			require.NoError(t, err, "one busy KB cannot prevent another KB's admission")
			require.NoError(t, other.Release(context.Background()))
			for l := range leases {
				require.NoError(t, l.Release(context.Background()))
			}
			next, err := acquire("wiki:kb-a")
			require.NoError(t, err)
			require.NoError(t, next.Release(context.Background()))
		})
	}
}

func TestRedisSlotCannotReviveAfterExpiryOrRelease(t *testing.T) {
	client := leaseRedis(t)
	ctx := context.Background()
	old, err := TrySlots(ctx, client, "wiki:kb", 1, time.Minute)
	require.NoError(t, err)
	token := client.ZRange(ctx, "wiki:kb", 0, -1).Val()[0]
	require.NoError(t, client.ZAdd(ctx, "wiki:kb", redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: token}).Err())
	next, err := TrySlots(ctx, client, "wiki:kb", 1, time.Minute)
	require.NoError(t, err)
	require.ErrorIs(t, old.Renew(ctx), ErrLost)
	require.NoError(t, old.Release(ctx))
	require.EqualValues(t, 1, client.ZCard(ctx, "wiki:kb").Val(), "old release must preserve the new owner's slot")
	require.NoError(t, next.Renew(ctx))
	require.NoError(t, next.Release(ctx))
	require.ErrorIs(t, next.Renew(ctx), ErrLost)
	require.Zero(t, client.ZCard(ctx, "wiki:kb").Val(), "late renewal must never recreate a released slot")
}

func TestRedisObjectCannotDeleteOrRenewReplacement(t *testing.T) {
	client := leaseRedis(t)
	ctx := context.Background()
	old, err := TryObject(ctx, client, "wiki:slug:kb:page", time.Minute)
	require.NoError(t, err)
	_, err = TryObject(ctx, client, "wiki:slug:kb:page", time.Minute)
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, client.Set(ctx, "wiki:slug:kb:page", "replacement", time.Minute).Err())
	require.ErrorIs(t, old.Renew(ctx), ErrLost)
	require.ErrorIs(t, old.Release(ctx), ErrLost)
	require.Equal(t, "replacement", client.Get(ctx, "wiki:slug:kb:page").Val())
}

func TestCanceledWorkStillReleasesItsOwnedClaim(t *testing.T) {
	client := leaseRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	h, err := Claim(ctx, func(ctx context.Context) (Lease, error) { return TrySlots(ctx, client, "wiki:cancel", 1, time.Minute) }, time.Second)
	require.NoError(t, err)
	cancel()
	require.NoError(t, h.Close())
	require.Zero(t, client.ZCard(context.Background(), "wiki:cancel").Val())
	called := false
	_, err = Claim(ctx, func(context.Context) (Lease, error) { called = true; return Funcs{}, nil }, 0)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}
