package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func (r *codeWikiRepository) ReadRunProgress(ctx context.Context, tenant uint64, kb, runID string) (*types.CodeWikiProgressSnapshot, error) {
	result := &types.CodeWikiProgressSnapshot{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot := &codeWikiRepository{db: tx}
		var err error
		result.Run, err = snapshot.GetRun(ctx, kb, runID)
		if err != nil {
			return err
		}
		if result.Run.TenantID != tenant {
			return types.ErrCodeWikiRunNotFound
		}
		result.Manifest, err = snapshot.ListRunRepositories(ctx, runID)
		if err != nil {
			return err
		}
		result.Pages, err = snapshot.ListRunPages(ctx, runID)
		if err != nil {
			return err
		}
		result.Events, err = snapshot.ListRunEvents(ctx, kb, runID, 0)
		if err != nil {
			return err
		}
		if result.Run.ExecutionEpoch > 0 {
			result.Topics, err = snapshot.ListTopicStates(ctx, tenant, kb, runID)
			if err != nil {
				return err
			}
			result.Run.CompletedPages, result.Run.FailedPages = 0, 0
			for _, topic := range result.Topics {
				if topic.Status == "completed" {
					result.Run.CompletedPages++
				}
				if topic.Status == "failed" {
					result.Run.FailedPages++
				}
			}
		}
		if result.Run.Status == types.CodeWikiRunStatusStaged {
			result.BaselinePages, err = snapshot.ListRawWikiPages(ctx, kb)
			if err != nil {
				return err
			}
		}
		// Opaque revision binds the response contents, including events and
		// topic changes that do not update the parent's timestamp.
		baselines := make([]string, 0, len(result.BaselinePages))
		for _, page := range result.BaselinePages {
			baselines = append(baselines, types.CodeWikiPageFingerprint(page))
		}
		blob, err := json.Marshal(struct {
			Progress  *types.CodeWikiProgressSnapshot
			Baselines []string
		}{result, baselines})
		if err != nil {
			return err
		}
		hash := sha256.Sum256(blob)
		result.Revision = hex.EncodeToString(hash[:16])
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
