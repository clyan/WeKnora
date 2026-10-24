package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Execution events describe work actually started, independently of the broad
// run phase. They carry no prompts, source contents, credentials or raw errors.
func (state *codeWikiRunState) beginActivity(ctx context.Context, message string) func(error) {
	if state.activity == nil {
		return func(error) {}
	}
	started := time.Now()
	state.activity(ctx, "operation_started", message)
	return func(err error) {
		kind, outcome := "operation_completed", "完成"
		if err != nil {
			kind, outcome = "operation_failed", "未完成"
		}
		state.activity(ctx, kind, fmt.Sprintf("%s · %s（%.1f 秒）", message, outcome, time.Since(started).Seconds()))
	}
}

func (state *codeWikiRunState) repositoryLabel(id string) string {
	if name := state.repoNames[id]; name != "" {
		return name
	}
	return id
}

func (s *codeWikiService) ListRunEvents(ctx context.Context, kbID, runID string, beforeID int64) ([]*types.CodeWikiRunEvent, error) {
	if _, err := s.GetRun(ctx, kbID, runID); err != nil {
		return nil, err
	}
	return s.repo.ListRunEvents(ctx, kbID, runID, beforeID)
}

func (s *codeWikiService) recordRunEvent(ctx context.Context, run *types.CodeWikiRun, kind, message string, retryAt *time.Time) {
	message = truncateCodeWikiString(message, 500)
	if err := s.repo.RecordRunEvent(ctx, &types.CodeWikiRunEvent{KnowledgeBaseID: run.KnowledgeBaseID,
		RunID: run.ID, Kind: kind, Message: message, RetryAt: retryAt}); err != nil {
		logger.Warnf(ctx, "code wiki: activity persistence failed for run %s: %v", run.ID, err)
	}
}

func (s *codeWikiService) heartbeatCodeWikiRun(ctx context.Context, runID, owner string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.repo.HeartbeatRun(ctx, runID, owner); err != nil {
				logger.Warnf(ctx, "code wiki: heartbeat failed for run %s: %v", runID, err)
			}
		}
	}
}
