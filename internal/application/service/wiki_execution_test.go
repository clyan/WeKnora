package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/common/tasklease"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func wikiExecutionFixture(t *testing.T) (*wikiIngestService, *redis.Client) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return &wikiIngestService{redisClient: client}, client
}

func TestWikiExecutionSharedSlotsKeepWikiLimitAndReleaseOnce(t *testing.T) {
	s, client := wikiExecutionFixture(t)
	ctx := context.Background()
	_, first, ok := s.reserveInflightSlot(ctx, "kb", 2)
	require.True(t, ok)
	_, second, ok := s.reserveInflightSlot(ctx, "kb", 2)
	require.True(t, ok)
	_, _, ok = s.reserveInflightSlot(ctx, "kb", 2)
	require.False(t, ok, "ordinary Wiki retains its own configured batch cap")
	_, other, ok := s.reserveInflightSlot(ctx, "other", 2)
	require.True(t, ok)
	first()
	first()
	require.EqualValues(t, 1, client.ZCard(ctx, wikiInflightPrefix+"kb").Val())
	second()
	other()
	require.Zero(t, client.ZCard(ctx, wikiInflightPrefix+"kb").Val())
	_, release, ok := (&wikiIngestService{}).reserveInflightSlot(ctx, "lite", 2)
	require.True(t, ok, "Lite continues to rely on its local batch guard")
	release()
}

func TestWikiExecutionSlugLockKeepsReplacementOwner(t *testing.T) {
	s, client := wikiExecutionFixture(t)
	ctx := context.Background()
	owned, err := s.withSlugLock(ctx, "kb", "page", func(lockCtx context.Context) error {
		require.NoError(t, lockCtx.Err())
		return client.Set(ctx, wikiSlugLockPrefix+"kb:page", "replacement", wikiSlugLockTTL).Err()
	})
	require.True(t, owned)
	require.ErrorIs(t, err, tasklease.ErrLost)
	require.Equal(t, "replacement", client.Get(ctx, wikiSlugLockPrefix+"kb:page").Val())
}

func TestWikiExecutionSlugLockPreservesCallbackFailureAndUnlocks(t *testing.T) {
	s, client := wikiExecutionFixture(t)
	ctx := context.Background()
	owned, err := s.withSlugLock(ctx, "kb", "page", func(context.Context) error { return context.Canceled })
	require.True(t, owned)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, client.Exists(ctx, wikiSlugLockPrefix+"kb:page").Val())
}
