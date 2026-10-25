package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiRecoveryPreservesCheckpointsAndFencesOldWorkers(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()
	run := &types.CodeWikiRun{TenantID: 7, KnowledgeBaseID: "kb", Status: "pending",
		ConfigSnapshot: types.JSON(`{"saved_files":256,"topics":27}`), ModelCalls: 3, TokensUsed: 1234}
	require.NoError(t, repo.CreateRun(ctx, run))
	ok, err := repo.TryAcquireRunLease(ctx, run.ID, "old-owner", 90)
	require.NoError(t, err)
	require.True(t, ok)
	old, err := repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	old.WriteLeaseOwner, old.Status = "old-owner", "planning"
	require.NoError(t, repo.UpdateRun(ctx, old))
	require.NoError(t, repo.RecoverInterruptedRuns(ctx, time.Now().UTC(), ""))
	fresh, err := repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "planning", fresh.Status, "a slow model with a live heartbeat is not interrupted")
	expired := time.Now().UTC().Add(-time.Second)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).UpdateColumn("lease_expires_at", expired).Error)
	require.NoError(t, repo.RecoverInterruptedRuns(ctx, time.Now().UTC(), ""))
	fresh, err = repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", fresh.Status)
	require.True(t, fresh.PauseRequested)
	require.Empty(t, fresh.LeaseOwner)
	require.Equal(t, string(run.ConfigSnapshot), string(fresh.ConfigSnapshot))
	require.Equal(t, 3, fresh.ModelCalls)
	require.EqualValues(t, 1234, fresh.TokensUsed)
	require.NoError(t, repo.RecoverInterruptedRuns(ctx, time.Now().UTC(), ""))
	events, err := repo.ListRunEvents(ctx, "kb", run.ID, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.ErrorIs(t, repo.HeartbeatRun(ctx, run.ID, "old-owner"), types.ErrCodeWikiRunLeaseLost)
	old.ConfigSnapshot = types.JSON(`{"late":true}`)
	require.ErrorIs(t, repo.UpdateRun(ctx, old), types.ErrCodeWikiRunLeaseLost)
	require.ErrorIs(t, repo.EnsureTopics(ctx, old, nil), types.ErrCodeWikiRunLeaseLost)
	require.ErrorIs(t, repo.BeginTopicPass(ctx, old, 10, time.Minute, 3), types.ErrCodeWikiRunLeaseLost)
	require.NoError(t, repo.RequestRunControl(ctx, "kb", run.ID, "resume"))
	ok, err = repo.TryAcquireRunLease(ctx, run.ID, "new-owner", 90)
	require.NoError(t, err)
	require.True(t, ok)
	require.ErrorIs(t, repo.UpdateRun(ctx, old), types.ErrCodeWikiRunLeaseLost)
	fresh, err = repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "new-owner", fresh.LeaseOwner)
	require.Equal(t, string(run.ConfigSnapshot), string(fresh.ConfigSnapshot))
}

func TestCodeWikiRecoveryHonorsCancelLegacyHeartbeatAndShutdownOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, owner, prefix, want   string
		legacy, live, cancel, topic bool
	}{
		{name: "legacy-dead", owner: "host-1", legacy: true, want: "paused"},
		{name: "legacy-live", owner: "host-1", legacy: true, live: true, want: "planning"},
		{name: "cancel", owner: "host-1", cancel: true, want: "canceled"},
		{name: "own-shutdown", owner: "host-1-attempt", prefix: "host-1-", live: true, want: "paused"},
		{name: "other-host", owner: "host-2-attempt", prefix: "host-1-", live: true, want: "planning"},
		{name: "topics-have-own-recovery", owner: "host-1", topic: true, want: "generating"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newCodeWikiTestDB(t)
			repo := &codeWikiRepository{db: db}
			now := time.Now().UTC()
			last := now.Add(-2 * time.Minute)
			if tc.live {
				last = now
			}
			ttl := 90 * time.Second
			if tc.legacy {
				ttl = time.Duration(int(types.MaxCodeWikiRunTimeoutMinutesCap)*60+600) * time.Second
			}
			expires := last.Add(ttl)
			run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: "planning", LeaseOwner: tc.owner,
				LeaseExpiresAt: &expires, CancelRequested: tc.cancel}
			if tc.topic {
				run.Status, run.ExecutionEpoch = "generating", 1
			}
			require.NoError(t, repo.CreateRun(context.Background(), run))
			require.NoError(t, repo.RecoverInterruptedRuns(context.Background(), now, tc.prefix))
			fresh, err := repo.GetRun(context.Background(), "kb", run.ID)
			require.NoError(t, err)
			require.Equal(t, tc.want, fresh.Status)
		})
	}
}

func TestCodeWikiRecoveryDoesNotOverrideConcurrentHeartbeat(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	now := time.Now().UTC()
	expires := now.Add(-time.Second)
	run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: "planning", LeaseOwner: "owner", LeaseExpiresAt: &expires}
	require.NoError(t, repo.CreateRun(context.Background(), run))
	renewed := now.Add(90 * time.Second)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).UpdateColumn("lease_expires_at", renewed).Error)
	// Recovery holds an old observation; a heartbeat won the race.
	run.LeaseExpiresAt = &expires
	require.NoError(t, repo.pauseInterruptedRun(context.Background(), run, now))
	fresh, err := repo.GetRun(context.Background(), "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "planning", fresh.Status)
}
