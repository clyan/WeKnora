package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errTopicLeaseLost = types.ErrCodeWikiTopicChanged

func (r *codeWikiRepository) AddTopicReview(ctx context.Context, tenant uint64, kb, runID, key string, revision int, review types.CodeWikiTopicReview) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, tenant, kb, runID)
		if err != nil {
			return err
		}
		if run.Status != types.CodeWikiRunStatusPaused && run.Status != types.CodeWikiRunStatusStaged && run.Status != types.CodeWikiRunStatusPublished {
			return fmt.Errorf("请等待当前分析暂停或结束后再登记复核")
		}
		var topic types.CodeWikiTopic
		if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ?", tenant, kb, runID, key).First(&topic).Error; err != nil {
			return err
		}
		if topic.Revision != revision {
			return errTopicLeaseLost
		}
		var cp types.CodeWikiTopicCheckpoint
		if json.Unmarshal(topic.Checkpoint, &cp) != nil || cp.Version != 1 {
			return fmt.Errorf("主题分析版本无法读取")
		}
		review.AnalysisRevision = revision
		cp.Reviews = append(cp.Reviews, review)
		blob, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		// Review metadata never edits conclusions or promotes explanations to facts.
		return tx.Model(&topic).Update("checkpoint", types.JSON(blob)).Error
	})
}

func topicRun(tx *gorm.DB, tenant uint64, kb, run string) (*types.CodeWikiRun, error) {
	var row types.CodeWikiRun
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", tenant, kb, run).First(&row).Error
	return &row, err
}

func (r *codeWikiRepository) EnsureTopics(ctx context.Context, run *types.CodeWikiRun, rows []*types.CodeWikiTopic) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := topicRun(tx, run.TenantID, run.KnowledgeBaseID, run.ID); err != nil {
			return err
		}
		for _, row := range rows {
			if row.TenantID != run.TenantID || row.KnowledgeBaseID != run.KnowledgeBaseID || row.RunID != run.ID {
				return fmt.Errorf("topic scope mismatch")
			}
			if row.ID == "" {
				row.ID = uuid.NewString()
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "topic_key"}}, DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *codeWikiRepository) ListTopics(ctx context.Context, tenant uint64, kb, run string) ([]*types.CodeWikiTopic, error) {
	var rows []*types.CodeWikiTopic
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ?", tenant, kb, run).Order("created_at, topic_key").Find(&rows).Error
	return rows, err
}

func (r *codeWikiRepository) ListTopicStates(ctx context.Context, tenant uint64, kb, run string) ([]*types.CodeWikiTopic, error) {
	var rows []*types.CodeWikiTopic
	err := r.db.WithContext(ctx).Omit("Checkpoint").Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND status <> ?", tenant, kb, run, "superseded").Order("created_at, topic_key").Find(&rows).Error
	return rows, err
}

func (r *codeWikiRepository) GetTopic(ctx context.Context, tenant uint64, kb, run, key string) (*types.CodeWikiTopic, error) {
	var topic types.CodeWikiTopic
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ?", tenant, kb, run, key).First(&topic).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiTopicNotFound
	}
	return &topic, err
}

func (r *codeWikiRepository) BeginTopicPass(ctx context.Context, run *types.CodeWikiRun, calls int, timeout time.Duration, concurrency int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := topicRun(tx, run.TenantID, run.KnowledgeBaseID, run.ID)
		if err != nil {
			return err
		}
		if fresh.CancelRequested || types.IsTerminalCodeWikiRunStatus(fresh.Status) {
			return errTopicLeaseLost
		}
		now := time.Now().UTC()
		deadline := now.Add(timeout)
		fresh.ExecutionEpoch++
		fresh.ModelCallLimit = fresh.ModelCalls + calls
		fresh.ExecutionDeadline = &deadline
		if concurrency < 1 {
			concurrency = types.DefaultCodeWikiTopicConcurrency
		}
		fresh.TopicConcurrency = concurrency
		// A completed guide depends on the exact completed topic revisions.
		// Retrying a source topic invalidates only this derived candidate.
		var pendingSources int64
		if err := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key <> ? AND status IN ?", run.TenantID, run.KnowledgeBaseID, run.ID, types.CodeWikiSummaryTopicKey, []string{"queued", "waiting", "paused", "running"}).Count(&pendingSources).Error; err != nil {
			return err
		}
		if pendingSources > 0 {
			result := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ? AND status = ?", run.TenantID, run.KnowledgeBaseID, run.ID, types.CodeWikiSummaryTopicKey, "completed").Updates(map[string]any{"status": "queued", "revision": gorm.Expr("revision + 1"), "action": "等待其他主题结束，再汇总项目概览", "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				if err := tx.Model(&types.CodeWikiRunPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND page_key = ?", run.TenantID, run.KnowledgeBaseID, run.ID, types.CodeWikiSummaryTopicKey).Updates(map[string]any{"status": types.CodeWikiRunPageStatusPending, "content": "", "evidence": types.JSON(`[]`), "validation_status": "pending", "updated_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(fresh).Updates(map[string]any{"execution_epoch": fresh.ExecutionEpoch, "model_call_limit": fresh.ModelCallLimit,
			"execution_deadline": deadline, "topic_concurrency": concurrency, "status": types.CodeWikiRunStatusGenerating,
			"phase": "正在按主题分析源码并生成文档", "finished_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND status IN ?", run.ID, []string{"paused", "waiting"}).
			Updates(map[string]any{"status": "queued", "lease_owner": "", "lease_expires_at": nil, "action": "等待继续分析", "updated_at": now}).Error; err != nil {
			return err
		}
		*run = *fresh
		run.Status = types.CodeWikiRunStatusGenerating
		return nil
	})
}

func topicPassValid(run *types.CodeWikiRun, epoch int) bool {
	return run.ExecutionEpoch == epoch && run.Status == types.CodeWikiRunStatusGenerating && !run.CancelRequested && !run.PauseRequested &&
		(run.ExecutionDeadline == nil || time.Now().Before(*run.ExecutionDeadline))
}

func (r *codeWikiRepository) ClaimTopic(ctx context.Context, tenant uint64, kb, runID, key string, epoch, revision int, owner string) (*types.CodeWikiTopic, error) {
	var claimed *types.CodeWikiTopic
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, tenant, kb, runID)
		if err != nil {
			return err
		}
		if !topicPassValid(run, epoch) || (run.ModelCalls >= run.ModelCallLimit && key != types.CodeWikiSummaryTopicKey) {
			return nil
		}
		now := time.Now().UTC()
		var active int64
		if err := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND status = 'running' AND lease_expires_at > ?", tenant, kb, now).Count(&active).Error; err != nil {
			return err
		}
		if active >= int64(run.TopicConcurrency) {
			return nil
		}
		var row types.CodeWikiTopic
		if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ?", tenant, kb, runID, key).First(&row).Error; err != nil {
			return err
		}
		if row.Revision != revision || (row.Status != "queued" && row.Status != "waiting" && !(row.Status == "running" && row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(now))) {
			return nil
		}
		expires := now.Add(90 * time.Second)
		row.Status, row.LeaseOwner, row.AttemptID, row.LeaseExpiresAt = "running", owner, uuid.NewString(), &expires
		if err := tx.Model(&row).Updates(map[string]any{"status": row.Status, "lease_owner": owner, "attempt_id": row.AttemptID, "lease_expires_at": expires, "updated_at": now}).Error; err != nil {
			return err
		}
		claimed = &row
		return nil
	})
	return claimed, err
}

func topicWrite(tx *gorm.DB, topic *types.CodeWikiTopic, epoch, revision int) (*gorm.DB, error) {
	run, err := topicRun(tx, topic.TenantID, topic.KnowledgeBaseID, topic.RunID)
	if err != nil {
		return nil, err
	}
	if run.ExecutionEpoch != epoch || run.Status != types.CodeWikiRunStatusGenerating {
		return nil, errTopicLeaseLost
	}
	return tx.Model(&types.CodeWikiTopic{}).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND lease_owner = ? AND attempt_id = ? AND revision = ? AND lease_expires_at > ?",
		topic.ID, topic.TenantID, topic.KnowledgeBaseID, topic.RunID, topic.LeaseOwner, topic.AttemptID, revision, time.Now().UTC()), nil
}

func (r *codeWikiRepository) SaveTopic(ctx context.Context, topic *types.CodeWikiTopic, epoch, revision int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query, err := topicWrite(tx, topic, epoch, revision)
		if err != nil {
			return err
		}
		result := query.Updates(map[string]any{"checkpoint": topic.Checkpoint, "status": topic.Status, "action": topic.Action, "error_message": topic.ErrorMessage,
			"revision": revision + 1, "lease_owner": "", "lease_expires_at": nil, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		topic.Revision = revision + 1
		return nil
	})
}

func (r *codeWikiRepository) HeartbeatTopic(ctx context.Context, topic *types.CodeWikiTopic, epoch int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, topic.TenantID, topic.KnowledgeBaseID, topic.RunID)
		if err != nil {
			return err
		}
		if run.CancelRequested || run.PauseRequested {
			return errTopicLeaseLost
		}
		query, err := topicWrite(tx, topic, epoch, topic.Revision)
		if err != nil {
			return err
		}
		updates := map[string]any{"lease_expires_at": time.Now().UTC().Add(90 * time.Second), "updated_at": time.Now().UTC()}
		if topic.Action != "" {
			updates["action"] = topic.Action
		}
		res := query.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		return nil
	})
}

func (r *codeWikiRepository) StageTopic(ctx context.Context, topic *types.CodeWikiTopic, epoch, revision int, page *types.CodeWikiRunPage) error {
	needsRecheck := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query, err := topicWrite(tx, topic, epoch, revision)
		if err != nil {
			return err
		}
		run, err := topicRun(tx, topic.TenantID, topic.KnowledgeBaseID, topic.RunID)
		if err != nil {
			return err
		}
		// An in-flight result may finish while pausing or after the final
		// reserved call. New claims are stopped, but valid work is preserved.
		if run.CancelRequested || page.RunID != run.ID || page.KnowledgeBaseID != run.KnowledgeBaseID || page.TenantID != run.TenantID || page.PageKey != topic.TopicKey {
			return errTopicLeaseLost
		}
		if topic.TopicKey != types.CodeWikiSummaryTopicKey {
			var cp types.CodeWikiTopicCheckpoint
			if json.Unmarshal(topic.Checkpoint, &cp) != nil || cp.Version != 1 {
				return fmt.Errorf("主题分析记录无法读取")
			}
			var peers []*types.CodeWikiTopic
			if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND status = ? AND topic_key <> ?", run.TenantID, run.KnowledgeBaseID, run.ID, "completed", topic.TopicKey).Order("topic_key").Find(&peers).Error; err != nil {
				return err
			}
			if questions := types.CodeWikiConsistencyQuestions(topic.TopicKey, &cp, peers); len(questions) > 0 {
				cp.Items = append(cp.Items, questions...)
				cp.Validation = nil
				cp.Feedback = "New consistency questions compare the same source node with another completed topic. Read the peer items using topic_items and re-read source; explicitly record compatibility/correction or an unresolved discrepancy. Preserve both origins."
				blob, _ := json.Marshal(cp)
				result := query.Updates(map[string]any{"status": "queued", "action": "正在核对公共实现的结论差异", "checkpoint": types.JSON(blob), "revision": revision + 1, "lease_owner": "", "lease_expires_at": nil, "updated_at": time.Now().UTC()})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return errTopicLeaseLost
				}
				topic.Revision, topic.Status = revision+1, "queued"
				needsRecheck = true
				return nil
			}
		}
		res := query.Updates(map[string]any{"status": "completed", "action": "文档已生成，等待审阅", "checkpoint": topic.Checkpoint, "revision": revision + 1, "lease_owner": "", "lease_expires_at": nil, "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		res = tx.Model(&types.CodeWikiRunPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND page_key = ? AND status = ?", page.TenantID, page.KnowledgeBaseID, page.RunID, page.PageKey, types.CodeWikiRunPageStatusPending).
			Updates(map[string]any{"content": page.Content, "evidence": page.Evidence, "validation_status": page.ValidationStatus, "status": page.Status, "generation_error": page.GenerationError, "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		return nil
	})
	if err == nil && needsRecheck {
		return types.ErrCodeWikiTopicNeedsRecheck
	}
	return err
}

func (r *codeWikiRepository) RefreshTopicRun(ctx context.Context, tenant uint64, kb, runID string, epoch int) (*types.CodeWikiRun, error) {
	var result *types.CodeWikiRun
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, tenant, kb, runID)
		if err != nil {
			return err
		}
		result = run
		if run.ExecutionEpoch != epoch || run.Status != types.CodeWikiRunStatusGenerating {
			return nil
		}
		now := time.Now().UTC()
		base := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ?", tenant, kb, runID)
		if err := base.Where("status = 'running' AND lease_expires_at <= ?", now).Updates(map[string]any{"status": "queued", "lease_owner": "", "lease_expires_at": nil, "action": "执行已中断，等待恢复", "updated_at": now}).Error; err != nil {
			return err
		}
		var rows []*types.CodeWikiTopic
		if err := tx.Omit("Checkpoint").Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ?", tenant, kb, runID).Find(&rows).Error; err != nil {
			return err
		}
		completed, failed, active, pending, paused, pendingAnalysis := 0, 0, 0, 0, 0, 0
		for _, row := range rows {
			if row.Status == "superseded" {
				continue
			}
			if row.TopicKey != types.CodeWikiSummaryTopicKey && (row.Status == "queued" || row.Status == "waiting" || row.Status == "paused") {
				pendingAnalysis++
			}
			switch row.Status {
			case "completed":
				completed++
			case "failed":
				failed++
			case "running":
				active++
			case "paused":
				pending++
				paused++
			default:
				pending++
			}
		}
		run.CompletedPages, run.FailedPages = completed, failed
		if run.ExecutionDeadline != nil && !now.Before(*run.ExecutionDeadline) {
			run.PauseRequested = true
			run.Phase = "本次执行时间已到，正在保存进度"
		}
		if active == 0 && paused > 0 {
			run.PauseRequested = true
		}
		if active == 0 && run.ModelCalls >= run.ModelCallLimit && pendingAnalysis > 0 {
			run.PauseRequested = true
			run.Phase = "本次生成已达到用量上限，正在保存进度"
		}
		if run.CancelRequested || run.PauseRequested {
			if err := tx.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND status IN ?", runID, []string{"queued", "waiting"}).Updates(map[string]any{"status": "paused", "action": "进度已保存"}).Error; err != nil {
				return err
			}
			if active == 0 {
				run.Status, run.Phase = types.CodeWikiRunStatusPaused, "已暂停，进度已保存，可继续"
				if run.ModelCalls >= run.ModelCallLimit {
					run.Phase = "本次生成已达到用量上限，进度已保存，可继续"
				}
				if run.CancelRequested {
					run.Status, run.Phase = types.CodeWikiRunStatusCanceled, "已取消"
					run.FinishedAt = &now
				}
			} else {
				run.Phase = "正在结束执行并保存进度"
			}
		} else if pending == 0 && active == 0 && len(rows) > 0 {
			run.FinishedAt = &now
			if failed > 0 {
				run.Status = types.CodeWikiRunStatusPaused
				run.Phase = "部分主题生成失败，成功文档已保留，请重试失败主题"
			} else {
				run.Status = types.CodeWikiRunStatusStaged
				run.Phase = "文档已生成，等待审阅与发布"
			}
		}
		return tx.Model(run).Updates(map[string]any{"completed_pages": completed, "failed_pages": failed, "status": run.Status, "phase": run.Phase, "pause_requested": run.PauseRequested, "finished_at": run.FinishedAt, "updated_at": now}).Error
	})
	return result, err
}

// Guard row serializes reservations across workers and model IDs sharing credentials.
func (r *codeWikiRepository) ReserveTopicModelCall(ctx context.Context, run *types.CodeWikiRun, slot string, limit int) (bool, error) {
	reserved := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := topicRun(tx, run.TenantID, run.KnowledgeBaseID, run.ID)
		if err != nil {
			return err
		}
		if !topicPassValid(fresh, run.ExecutionEpoch) || fresh.ModelCalls >= fresh.ModelCallLimit {
			return errTopicLeaseLost
		}
		// The prefix preceding '/' is a non-reversible credential-scope digest.
		group := run.ModelID
		for i, c := range slot {
			if c == '/' {
				group = slot[:i]
				break
			}
		}
		guard := types.CodeWikiModelSlot{ID: group, ModelID: group, ExpiresAt: time.Unix(0, 0).UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", group).First(&guard).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Where("model_id = ? AND id <> ? AND expires_at <= ?", group, group, now).Delete(&types.CodeWikiModelSlot{}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&types.CodeWikiModelSlot{}).Where("model_id = ? AND expires_at > ?", group, now).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit) {
			return nil
		}
		if err := tx.Create(&types.CodeWikiModelSlot{ID: slot, RunID: run.ID, TenantID: run.TenantID, ModelID: group, ExpiresAt: now.Add(5 * time.Minute)}).Error; err != nil {
			return err
		}
		res := tx.Model(fresh).Where("model_calls < model_call_limit").UpdateColumn("model_calls", gorm.Expr("model_calls + 1"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		run.ModelCalls = fresh.ModelCalls + 1
		reserved = true
		return nil
	})
	return reserved, err
}

func (r *codeWikiRepository) FinishTopicModelCall(ctx context.Context, run *types.CodeWikiRun, slot string, tokens int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := topicRun(tx, run.TenantID, run.KnowledgeBaseID, run.ID); err != nil {
			return err
		}
		deleted := tx.Where("id = ? AND run_id = ? AND tenant_id = ?", slot, run.ID, run.TenantID).Delete(&types.CodeWikiModelSlot{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return nil // Duplicate settlement must not count tokens twice.
		}
		return tx.Model(&types.CodeWikiRun{}).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", run.ID, run.TenantID, run.KnowledgeBaseID).
			UpdateColumn("tokens_used", gorm.Expr("tokens_used + ?", tokens)).Error
	})
}

func (r *codeWikiRepository) RefundTopicModelCall(ctx context.Context, run *types.CodeWikiRun, slot string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := topicRun(tx, run.TenantID, run.KnowledgeBaseID, run.ID); err != nil {
			return err
		}
		deleted := tx.Where("id = ? AND run_id = ? AND tenant_id = ?", slot, run.ID, run.TenantID).Delete(&types.CodeWikiModelSlot{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return nil
		}
		return tx.Model(&types.CodeWikiRun{}).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND model_calls > 0", run.ID, run.TenantID, run.KnowledgeBaseID).UpdateColumn("model_calls", gorm.Expr("model_calls - 1")).Error
	})
}

func (r *codeWikiRepository) RetryTopic(ctx context.Context, tenant uint64, kb, runID, key string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, tenant, kb, runID)
		if err != nil {
			return err
		}
		if run.Status != types.CodeWikiRunStatusPaused || run.CancelRequested {
			return errTopicLeaseLost
		}
		res := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ? AND status = 'failed'", tenant, kb, runID, key).
			Updates(map[string]any{"status": "paused", "error_message": "", "action": "等待重新分析", "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errTopicLeaseLost
		}
		return nil
	})
}
