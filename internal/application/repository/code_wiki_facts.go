package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func factRun(db *gorm.DB, scope types.CodeWikiFactScope, lock bool) (*types.CodeWikiRun, error) {
	if scope.TenantID == 0 || scope.KnowledgeBaseID == "" || scope.RunID == "" {
		return nil, fmt.Errorf("fact operations require tenant, knowledge base and run scope")
	}
	query := db.Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", scope.TenantID, scope.KnowledgeBaseID, scope.RunID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var run types.CodeWikiRun
	if err := query.First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// AppendFactRecords is idempotent only for identical payloads. It never
// overwrites a fact on retry or edits a published/superseded version.
func (r *codeWikiRepository) AppendFactRecords(ctx context.Context, scope types.CodeWikiFactScope, records []*types.CodeWikiFactRecord) error {
	if len(records) > 500 {
		return fmt.Errorf("fact append batch exceeds limit")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := factRun(tx, scope, true)
		if err != nil {
			return err
		}
		switch run.Status {
		case types.CodeWikiRunStatusPending, types.CodeWikiRunStatusAnalyzing, types.CodeWikiRunStatusPlanning, types.CodeWikiRunStatusGenerating, types.CodeWikiRunStatusPaused:
		default:
			return fmt.Errorf("fact version is sealed")
		}
		for _, record := range records {
			if record == nil || record.TenantID != scope.TenantID || record.KnowledgeBaseID != scope.KnowledgeBaseID || record.RunID != scope.RunID || record.SchemaVersion != types.CodeWikiFactSchemaVersion || strings.TrimSpace(record.RecordKey) == "" || len(record.RecordKey) > 200 || len(record.Payload) > 128<<10 {
				return fmt.Errorf("invalid fact envelope or scope")
			}
			switch record.RecordType {
			case "source", "fact", "evidence", "relation", "document_link", "question", "human_explanation", "review":
			default:
				return fmt.Errorf("unknown fact record type")
			}
			var payload any
			if json.Unmarshal(record.Payload, &payload) != nil || payload == nil {
				return fmt.Errorf("invalid fact payload")
			}
			if err := validateFactPayload(record); err != nil {
				return err
			}
			canonical, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			record.Payload = types.JSON(canonical)
			var existing types.CodeWikiFactRecord
			err = tx.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND record_type = ? AND record_key = ?", scope.TenantID, scope.KnowledgeBaseID, scope.RunID, record.RecordType, record.RecordKey).First(&existing).Error
			if err == nil {
				var oldPayload any
				if json.Unmarshal(existing.Payload, &oldPayload) != nil {
					return fmt.Errorf("stored fact payload is invalid")
				}
				oldCanonical, _ := json.Marshal(oldPayload)
				if existing.SchemaVersion != record.SchemaVersion || string(oldCanonical) != string(canonical) {
					return fmt.Errorf("fact identity already has a different payload")
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			record.ID = uuid.NewString()
			if err := tx.Create(record).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func validateFactPayload(record *types.CodeWikiFactRecord) error {
	switch record.RecordType {
	case "fact":
		var fact types.CodeWikiFact
		if json.Unmarshal(record.Payload, &fact) != nil || fact.Kind == "" || fact.Subject == "" || len(fact.Evidence) == 0 || !json.Valid(fact.Attributes) {
			return fmt.Errorf("facts require a subject, structured attributes and implementation evidence")
		}
		if fact.State != "syntax_candidate" && fact.State != "code_supported" && fact.State != "unresolved" {
			return fmt.Errorf("human explanations cannot be stored as code facts")
		}
	case "evidence":
		var evidence types.CodeWikiFactEvidence
		if json.Unmarshal(record.Payload, &evidence) != nil || evidence.RepositoryID == "" || evidence.SnapshotID == "" || evidence.CommitSHA == "" || evidence.Path == "" || evidence.StartLine < 1 || evidence.EndLine < evidence.StartLine {
			return fmt.Errorf("invalid source evidence")
		}
	case "review", "human_explanation":
		var review types.CodeWikiFactReview
		if json.Unmarshal(record.Payload, &review) != nil || review.FactKey == "" || review.ActorID == "" || review.RecordedAt.IsZero() || review.Conclusion == "" {
			return fmt.Errorf("human records require a fact, actor, time and conclusion")
		}
	}
	return nil
}

// Pagination uses the immutable record key, never an offset into a changing set.
func (r *codeWikiRepository) ListFactRecords(ctx context.Context, scope types.CodeWikiFactScope, recordType, after string, limit int) ([]*types.CodeWikiFactRecord, error) {
	db := r.db.WithContext(ctx)
	if _, err := factRun(db, scope, false); err != nil {
		return nil, err
	}
	if recordType == "" || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("fact queries require a record type and limit between 1 and 500")
	}
	var records []*types.CodeWikiFactRecord
	err := db.Where("tenant_id = ? AND knowledge_base_id = ? AND run_id = ? AND record_type = ? AND record_key > ?", scope.TenantID, scope.KnowledgeBaseID, scope.RunID, recordType, after).
		Order("record_key ASC").Limit(limit).Find(&records).Error
	return records, err
}
