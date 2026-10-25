package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const codeWikiControlPollInterval = 500 * time.Millisecond

func (s *codeWikiService) readRunControl(ctx context.Context, identity *types.CodeWikiRun, checkEpoch bool) error {
	current, err := s.repo.GetRunControl(ctx, identity.KnowledgeBaseID, identity.ID)
	if err != nil || current == nil {
		return fmt.Errorf("%w: 无法确认任务控制状态，已中断当前执行", errCodeWikiRunPaused)
	}
	if identity.WriteLeaseOwner != "" && (current.LeaseOwner != identity.WriteLeaseOwner ||
		current.LeaseExpiresAt == nil || !current.LeaseExpiresAt.After(time.Now())) {
		return types.ErrCodeWikiRunLeaseLost
	}
	switch {
	case current.TenantID != identity.TenantID || (checkEpoch && current.ExecutionEpoch != identity.ExecutionEpoch),
		current.CancelRequested, types.IsTerminalCodeWikiRunStatus(current.Status):
		return errCodeWikiRunCanceled
	case current.PauseRequested || current.Status == types.CodeWikiRunStatusPaused:
		return errCodeWikiRunPaused
	case checkEpoch && current.ExecutionDeadline != nil && !time.Now().Before(*current.ExecutionDeadline):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// Apply control to queued topics immediately as well. With no active workers,
// there is no request watcher to finish the transition; do not leave the UI
// waiting for the next periodic dispatcher merely to acknowledge a stop.
func (s *codeWikiService) refreshRunControl(ctx context.Context, run *types.CodeWikiRun) error {
	if run.Status != types.CodeWikiRunStatusGenerating || run.ExecutionEpoch == 0 {
		return nil
	}
	repo, err := s.topicRepository()
	if err != nil {
		return err
	}
	_, err = repo.RefreshTopicRun(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, run.ExecutionEpoch)
	return err
}

// Watch persisted controls independently of the current tool/model call. This
// also reaches workers in other processes; a stage boundary or lease heartbeat
// must not be the only opportunity to interrupt a long request.
func (s *codeWikiService) watchRunControl(ctx context.Context, run *types.CodeWikiRun, checkEpoch bool) (context.Context, func()) {
	// executeRun mutates run as it progresses (including beginning a new topic
	// epoch). Only capture immutable identity; never read its mutable snapshot
	// from the watcher goroutine. Topic workers additionally fence their epoch.
	identity := types.CodeWikiRun{TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID, ID: run.ID, ExecutionEpoch: run.ExecutionEpoch, WriteLeaseOwner: run.WriteLeaseOwner}
	workCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(codeWikiControlPollInterval)
		defer ticker.Stop()
		for {
			checkCtx, stop := context.WithTimeout(workCtx, 2*time.Second)
			err := s.readRunControl(checkCtx, &identity, checkEpoch)
			stop()
			if workCtx.Err() != nil {
				return
			}
			if err != nil {
				// Stop rather than continue spending when control state cannot
				// be verified. The persisted checkpoint remains resumable.
				cancel(err)
				return
			}
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return workCtx, func() { cancel(context.Canceled); <-done }
}

// Providers commonly return context.Canceled. Keep the user's actual control
// reason so cancellation is not recorded as a model failure or worker crash.
func codeWikiExecutionError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}
