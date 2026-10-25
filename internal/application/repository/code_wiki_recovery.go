package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func codeWikiDiscoveryStatuses() []string {
	return []string{types.CodeWikiRunStatusPreparing, types.CodeWikiRunStatusAnalyzing,
		types.CodeWikiRunStatusPlanning, types.CodeWikiRunStatusGenerating, types.CodeWikiRunStatusValidating}
}

func discoveryLeaseValid(worker, current *types.CodeWikiRun) bool {
	if worker.WriteLeaseOwner == "" {
		return true // Topic workers use their own execution epoch and attempt lease.
	}
	return worker.WriteLeaseOwner == current.LeaseOwner && current.LeaseExpiresAt != nil &&
		current.LeaseExpiresAt.After(time.Now()) && current.Status != types.CodeWikiRunStatusPaused &&
		!types.IsTerminalCodeWikiRunStatus(current.Status)
}

// Recovery changes only execution state, never the saved configuration or
// candidates. Topic execution already has independent leases and recovery.
func (r *codeWikiRepository) RecoverInterruptedRuns(ctx context.Context, now time.Time, ownerPrefix string) error {
	var after string
	for {
		var runs []*types.CodeWikiRun
		query := r.db.WithContext(ctx).Where("status IN ? AND NOT (status = ? AND execution_epoch > 0) AND id > ?",
			codeWikiDiscoveryStatuses(), types.CodeWikiRunStatusGenerating, after)
		if ownerPrefix != "" {
			// Prefix matching happens in Go: host names are not SQL patterns.
			query = query.Where("lease_owner <> ''")
		}
		if err := query.Select("id", "tenant_id", "knowledge_base_id", "status", "lease_owner", "lease_expires_at", "updated_at", "execution_epoch", "cancel_requested").Order("id").Limit(100).Find(&runs).Error; err != nil {
			return err
		}
		for _, run := range runs {
			after = run.ID
			if ownerPrefix != "" {
				if !strings.HasPrefix(run.LeaseOwner, ownerPrefix) {
					continue
				}
			} else {
				last := run.UpdatedAt
				if run.LeaseExpiresAt != nil {
					last = types.CodeWikiRunHeartbeatAt(*run.LeaseExpiresAt, now)
				}
				if last.After(now.Add(-time.Duration(types.CodeWikiRunLeaseTTLSeconds) * time.Second)) {
					continue
				}
			}
			if err := r.pauseInterruptedRun(ctx, run, now); err != nil {
				return err
			}
		}
		if len(runs) < 100 {
			return nil
		}
	}
}

func (r *codeWikiRepository) pauseInterruptedRun(ctx context.Context, run *types.CodeWikiRun, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&types.CodeWikiRun{}).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND status = ? AND lease_owner = ? AND execution_epoch = ? AND updated_at = ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.Status, run.LeaseOwner, run.ExecutionEpoch, run.UpdatedAt)
		if run.LeaseExpiresAt == nil {
			query = query.Where("lease_expires_at IS NULL")
		} else {
			query = query.Where("lease_expires_at = ?", *run.LeaseExpiresAt)
		}
		status, phase, kind := types.CodeWikiRunStatusPaused, "执行已中断，进度已保存，点击继续生成", "worker_interrupted"
		updates := map[string]any{"status": status, "phase": phase, "pause_requested": true,
			"lease_owner": "", "lease_expires_at": nil, "error_message": "", "updated_at": now}
		if run.CancelRequested {
			status, phase, kind = types.CodeWikiRunStatusCanceled, "已取消，已发布文档保持原样", "canceled"
			updates["status"], updates["phase"], updates["finished_at"] = status, phase, now
		}
		result := query.Updates(updates)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return tx.Create(&types.CodeWikiRunEvent{KnowledgeBaseID: run.KnowledgeBaseID, RunID: run.ID,
			Kind: kind, Message: phase, CreatedAt: now}).Error
	})
}
