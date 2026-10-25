package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"
)

// Apply a complete directory change and ownership transfer under the same run
// lock used by claims, checkpoints and staging. Unrelated workers may continue;
// another active source topic must first finish its bounded step.
func (r *codeWikiRepository) ApplyTopicPlan(ctx context.Context, current *types.CodeWikiTopic, epoch, revision int, update *types.CodeWikiTopicPlanUpdate) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := topicRun(tx, current.TenantID, current.KnowledgeBaseID, current.RunID)
		if err != nil {
			return err
		}
		if !topicPassValid(run, epoch) {
			return errTopicLeaseLost
		}
		query, err := topicWrite(tx, current, epoch, revision)
		if err != nil {
			return err
		}
		var ownCount int64
		if err := query.Count(&ownCount).Error; err != nil {
			return err
		}
		if ownCount != 1 {
			return errTopicLeaseLost
		}
		if update == nil || len(update.Topics) == 0 || len(update.Topics) != len(update.Pages) {
			return fmt.Errorf("主题调整内容不完整")
		}
		sources := map[string]*types.CodeWikiTopic{}
		for _, ref := range update.Sources {
			var row types.CodeWikiTopic
			if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ?", run.TenantID, run.KnowledgeBaseID, run.ID, ref.Key).First(&row).Error; err != nil {
				return err
			}
			if row.Revision != ref.Revision || row.Status == "superseded" || (row.Status == "running" && row.TopicKey != current.TopicKey) {
				return errTopicLeaseLost
			}
			sources[ref.Key] = &row
		}
		if sources[current.TopicKey] == nil {
			return fmt.Errorf("主题调整必须绑定当前分析修订")
		}
		var frozen map[string]json.RawMessage
		if json.Unmarshal(run.ConfigSnapshot, &frozen) != nil {
			return fmt.Errorf("任务配置无法读取")
		}
		var guard types.CodeWikiPublicationGuard
		if json.Unmarshal(frozen["publication_guard"], &guard) != nil || guard.ReviewRequired || guard.ReviewApproved {
			return fmt.Errorf("当前发布范围需要重新审阅，不能自动调整主题")
		}
		retired := map[string]bool{}
		for _, key := range update.Retire {
			if sources[key] == nil || key == types.CodeWikiSummaryTopicKey {
				return fmt.Errorf("无法调整未确认修订的主题")
			}
			retired[key] = true
		}
		targets := make([]string, 0, len(update.Topics))
		seen := map[string]bool{}
		now := time.Now().UTC()
		for i, topic := range update.Topics {
			page := update.Pages[i]
			if topic.TenantID != run.TenantID || topic.KnowledgeBaseID != run.KnowledgeBaseID || topic.RunID != run.ID || topic.TopicKey == "" || topic.TopicKey == types.CodeWikiSummaryTopicKey || seen[topic.TopicKey] || page.PageKey != topic.TopicKey || page.TenantID != run.TenantID || page.KnowledgeBaseID != run.KnowledgeBaseID || page.RunID != run.ID {
				return fmt.Errorf("主题调整范围不匹配")
			}
			seen[topic.TopicKey] = true
			var exists int64
			if err := tx.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND topic_key = ?", run.ID, topic.TopicKey).Count(&exists).Error; err != nil {
				return err
			}
			if exists > 0 {
				return fmt.Errorf("主题标识已存在，请使用现有主题或重新划分范围")
			}
			topic.ID, page.ID = uuid.NewString(), uuid.NewString()
			topic.Status, page.Status, page.ValidationStatus = "queued", types.CodeWikiRunPageStatusPending, "pending"
			if err := tx.Create(topic).Error; err != nil {
				return err
			}
			if err := tx.Create(page).Error; err != nil {
				return err
			}
			targets = append(targets, topic.TopicKey)
		}
		for key := range retired {
			row := sources[key]
			if key == current.TopicKey {
				row.Checkpoint = current.Checkpoint
			}
			var cp types.CodeWikiTopicCheckpoint
			if json.Unmarshal(row.Checkpoint, &cp) != nil {
				return fmt.Errorf("来源主题分析记录无法读取")
			}
			cp.Successors, cp.PlanChange = targets, nil
			blob, _ := json.Marshal(cp)
			if err := tx.Model(row).Updates(map[string]any{"status": "superseded", "action": "主题范围已调整，原分析记录保留", "checkpoint": types.JSON(blob), "revision": gorm.Expr("revision + 1"), "lease_owner": "", "lease_expires_at": nil, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&types.CodeWikiRunPage{}).Where("run_id = ? AND page_key = ?", run.ID, key).Updates(map[string]any{"status": types.CodeWikiRunPageStatusSkipped, "validation_status": "superseded", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if !retired[current.TopicKey] {
			// Count mutates GORM's statement; build a fresh CAS update instead
			// of reusing its SELECT/FROM clauses for an UPDATE.
			query, err = topicWrite(tx, current, epoch, revision)
			if err != nil {
				return err
			}
			result := query.Updates(map[string]any{"checkpoint": current.Checkpoint, "status": "queued", "revision": revision + 1, "lease_owner": "", "lease_expires_at": nil, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errTopicLeaseLost
			}
		}
		// A previously completed guide is derived from the old directory and
		// cannot remain publishable after a scope change.
		guide := tx.Model(&types.CodeWikiTopic{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND topic_key = ? AND status = ?", run.TenantID, run.KnowledgeBaseID, run.ID, types.CodeWikiSummaryTopicKey, "completed").Updates(map[string]any{"status": "queued", "revision": gorm.Expr("revision + 1"), "action": "等待其他主题结束，再汇总项目概览", "updated_at": now})
		if guide.Error != nil {
			return guide.Error
		}
		if guide.RowsAffected > 0 {
			if err := tx.Model(&types.CodeWikiRunPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND page_key = ?", run.TenantID, run.KnowledgeBaseID, run.ID, types.CodeWikiSummaryTopicKey).Updates(map[string]any{"status": types.CodeWikiRunPageStatusPending, "content": "", "evidence": types.JSON(`[]`), "validation_status": "pending", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		var activePages []*types.CodeWikiRunPage
		if err := tx.Where("run_id = ? AND validation_status <> ?", run.ID, "superseded").Order("page_key").Find(&activePages).Error; err != nil {
			return err
		}
		guard.AllowedPages = nil
		for _, page := range activePages {
			guard.AllowedPages = append(guard.AllowedPages, page.PageKey)
		}
		guardJSON, _ := json.Marshal(guard)
		frozen["publication_guard"] = guardJSON
		config, err := json.Marshal(frozen)
		if err != nil {
			return err
		}
		if err := tx.Model(run).Updates(map[string]any{"config_snapshot": types.JSON(config), "total_pages": len(activePages), "updated_at": now}).Error; err != nil {
			return err
		}
		current.Revision = revision + 1
		current.Status = "queued"
		if retired[current.TopicKey] {
			current.Status = "superseded"
		}
		return nil
	})
}
