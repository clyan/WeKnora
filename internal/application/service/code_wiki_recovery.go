package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
)

func codeWikiWorkerPrefix() string {
	return fmt.Sprintf("%s-%d-", hostnameCodeWiki(), os.Getpid())
}

// RunRecovery only settles interrupted discovery workers; it does not enqueue
// model work. The existing topic dispatcher retains topic recovery ownership.
func (s *codeWikiService) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		checkCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		err := s.repo.RecoverInterruptedRuns(checkCtx, time.Now().UTC(), "")
		stop()
		if err != nil && ctx.Err() == nil {
			logger.Warnf(ctx, "code wiki interrupted-run recovery failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *codeWikiService) PauseOwnedRuns(ctx context.Context) error {
	return s.repo.RecoverInterruptedRuns(ctx, time.Now().UTC(), codeWikiWorkerPrefix())
}
