package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiRecoveryInterruptsLatePlanningModel(t *testing.T) {
	s, db, run, _ := topicServiceFixture(t)
	s.modelService = topicFlowModelService{}
	ctx := context.Background()
	expires := time.Now().UTC().Add(90 * time.Second)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"status": "planning", "execution_epoch": 0,
			"lease_owner": "old-attempt", "lease_expires_at": expires}).Error)
	run, err := s.repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	run.WriteLeaseOwner = "old-attempt"
	late := make(chan struct{})
	model := &codeWikiBlockingChat{started: make(chan struct{}), late: late}
	state := &codeWikiRunState{run: run, cfg: codeWikiRunConfig{MaxModelCalls: 100}, chat: model}
	workCtx, stop := s.watchRunControl(ctx, run, false)
	defer stop()
	result := make(chan error, 1)
	go func() {
		_, err := s.callModel(workCtx, state, "plan remaining files")
		result <- err
	}()
	select {
	case <-model.started:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	// Simulate a process that stopped heartbeating while its provider kept running.
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).
		UpdateColumn("lease_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	require.NoError(t, s.repo.RecoverInterruptedRuns(ctx, time.Now().UTC(), ""))
	select {
	case <-workCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("lost execution was not interrupted")
	}
	require.NoError(t, s.repo.RequestRunControl(ctx, "kb", run.ID, "resume"))
	claimed, err := s.repo.TryAcquireRunLease(ctx, run.ID, "new-attempt", 90)
	require.NoError(t, err)
	require.True(t, claimed)
	close(late)
	require.ErrorIs(t, <-result, types.ErrCodeWikiRunLeaseLost)
	// The old worker's finalizer must not fail the new execution.
	s.finalizeRunOnError(workCtx, run)
	fresh, err := s.repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "new-attempt", fresh.LeaseOwner)
	require.Equal(t, "preparing", fresh.Status)
	state.run.Status = "failed"
	require.ErrorIs(t, s.repo.UpdateRun(ctx, state.run), types.ErrCodeWikiRunLeaseLost)
}

func TestCodeWikiRecoveryQueueRedeliveryWaitsForExplicitResume(t *testing.T) {
	s, db, run, queue := topicServiceFixture(t)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"status": "paused", "execution_epoch": 0, "pause_requested": true, "lease_owner": ""}).Error)
	payload, err := json.Marshal(types.CodeWikiGeneratePayload{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID})
	require.NoError(t, err)
	require.NoError(t, s.ProcessCodeWikiGenerate(context.Background(), asynq.NewTask(types.TypeCodeWikiGenerate, payload)))
	fresh, err := s.repo.GetRun(context.Background(), "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", fresh.Status)
	require.Empty(t, fresh.LeaseOwner)
	require.Empty(t, queue.messages)
}

func TestCodeWikiRecoveryContextShutdownKeepsPlanningResumable(t *testing.T) {
	s, db, run, _ := topicServiceFixture(t)
	expires := time.Now().UTC().Add(90 * time.Second)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"status": "planning", "execution_epoch": 0, "lease_owner": "attempt", "lease_expires_at": expires}).Error)
	run, err := s.repo.GetRun(context.Background(), "kb", run.ID)
	require.NoError(t, err)
	run.WriteLeaseOwner = "attempt"
	checkpoint := string(run.ConfigSnapshot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.finalizeRunOnError(ctx, run)
	fresh, err := s.repo.GetRun(context.Background(), "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", fresh.Status)
	require.Equal(t, checkpoint, string(fresh.ConfigSnapshot))
	require.True(t, fresh.PauseRequested)
	require.Contains(t, fresh.Phase, "点击继续生成")
}
