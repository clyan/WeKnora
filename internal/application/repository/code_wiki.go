package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// codeWikiRepository implements interfaces.CodeWikiRepository.
type codeWikiRepository struct {
	db *gorm.DB
}

// NewCodeWikiRepository creates the code wiki repository.
func NewCodeWikiRepository(db *gorm.DB) interfaces.CodeWikiRepository {
	return &codeWikiRepository{db: db}
}

// txHandle adapts a *gorm.DB transaction to the narrow interfaces.TX view.
type txHandle struct{ tx *gorm.DB }

func (t txHandle) EnsureWikiFolderPath(kbID string, tenantID uint64, path []string, sortOrder int) (string, error) {
	parent, parentPath := types.WikiFolderRootID, ""
	for depth, name := range path {
		if name == "" || len(name) > 255 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\\x00") {
			return "", fmt.Errorf("invalid directory segment")
		}
		var folder types.WikiFolder
		err := t.tx.Where("knowledge_base_id = ? AND parent_id = ? AND name = ?", kbID, parent, name).First(&folder).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			folder = types.WikiFolder{ID: uuid.NewString(), TenantID: tenantID, KnowledgeBaseID: kbID,
				ParentID: parent, Name: name, Path: strings.TrimPrefix(parentPath+"/"+name, "/"), Depth: depth + 1, SortOrder: sortOrder}
			if err := t.tx.Create(&folder).Error; err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		parent, parentPath = folder.ID, folder.Path
	}
	return parent, nil
}

func (t txHandle) GetWikiPageBySlug(kbID, slug string) (*types.WikiPage, error) {
	var page types.WikiPage
	err := t.tx.Where("knowledge_base_id = ? AND slug = ?", kbID, slug).First(&page).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// isUniqueViolation reports a DB unique-index rejection across postgres and
// sqlite error shapes.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "23505")
}

// --- Analysis profiles ------------------------------------------------------

func (r *codeWikiRepository) GetAnalysisProfile(ctx context.Context, tenantID uint64, kbID string) (*types.CodeWikiAnalysisProfile, error) {
	var profile types.CodeWikiAnalysisProfile
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		First(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// First access: create the default builtin profile.
		profile = types.CodeWikiAnalysisProfile{
			ID:               uuid.NewString(),
			TenantID:         tenantID,
			KnowledgeBaseID:  kbID,
			ProviderKind:     types.CodeWikiProviderBuiltin,
			SnapshotDelivery: types.CodeWikiDeliveryLocal,
			ConfigVersion:    1,
		}
		if err := r.db.WithContext(ctx).Create(&profile).Error; err != nil {
			if isUniqueViolation(err) {
				// Concurrent first-read: fetch the winner's row.
				return r.GetAnalysisProfile(ctx, tenantID, kbID)
			}
			return nil, err
		}
		return &profile, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (r *codeWikiRepository) SaveAnalysisProfile(ctx context.Context, profile *types.CodeWikiAnalysisProfile) error {
	return r.db.WithContext(ctx).Save(profile).Error
}

// --- Repository bindings ----------------------------------------------------

func (r *codeWikiRepository) CreateRepository(ctx context.Context, repo *types.CodeWikiRepository) error {
	if repo.ID == "" {
		repo.ID = uuid.NewString()
	}
	err := r.db.WithContext(ctx).Create(repo).Error
	if isUniqueViolation(err) {
		return fmt.Errorf("repository binding already exists for this source/project/ref")
	}
	return err
}

func (r *codeWikiRepository) UpdateRepository(ctx context.Context, repo *types.CodeWikiRepository) error {
	return r.db.WithContext(ctx).
		Model(&types.CodeWikiRepository{}).
		Where("id = ? AND knowledge_base_id = ?", repo.ID, repo.KnowledgeBaseID).
		Select("display_name", "role", "ref", "include_paths", "exclude_patterns", "enabled", "updated_at").
		Updates(map[string]interface{}{
			"display_name":     repo.DisplayName,
			"role":             repo.Role,
			"ref":              repo.Ref,
			"include_paths":    repo.IncludePaths,
			"exclude_patterns": repo.ExcludePatterns,
			"enabled":          repo.Enabled,
			"updated_at":       time.Now().UTC(),
		}).Error
}

func (r *codeWikiRepository) GetRepository(ctx context.Context, kbID, id string) (*types.CodeWikiRepository, error) {
	var repo types.CodeWikiRepository
	err := r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		First(&repo).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiRepoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (r *codeWikiRepository) ListRepositories(ctx context.Context, filter types.CodeWikiRepositoryFilter) ([]*types.CodeWikiRepository, error) {
	query := r.db.WithContext(ctx).Model(&types.CodeWikiRepository{})
	if filter.KnowledgeBaseID != "" {
		query = query.Where("knowledge_base_id = ?", filter.KnowledgeBaseID)
	}
	if filter.SourceID != "" {
		query = query.Where("source_id = ?", filter.SourceID)
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}
	var out []*types.CodeWikiRepository
	err := query.Order("created_at ASC").Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) DeleteRepository(ctx context.Context, kbID, id string) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		Delete(&types.CodeWikiRepository{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return types.ErrCodeWikiRepoNotFound
	}
	return nil
}

func (r *codeWikiRepository) CountRepositories(ctx context.Context, kbID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.CodeWikiRepository{}).
		Where("knowledge_base_id = ?", kbID).Count(&count).Error
	return count, err
}

func (r *codeWikiRepository) UpdateRepositoryStatus(ctx context.Context, repo *types.CodeWikiRepository) error {
	return r.db.WithContext(ctx).
		Model(&types.CodeWikiRepository{}).
		Where("id = ?", repo.ID).
		Updates(map[string]interface{}{
			"status":           repo.Status,
			"last_snapshot_id": repo.LastSnapshotID,
			"last_commit_sha":  repo.LastCommitSHA,
			"last_sync_at":     repo.LastSyncAt,
			"error_message":    repo.ErrorMessage,
			"updated_at":       time.Now().UTC(),
		}).Error
}

// TryMarkSyncing flips a binding into syncing only when another submission
// hasn't already claimed it; false means a sync is already in flight.
func (r *codeWikiRepository) TryMarkSyncing(ctx context.Context, id string) (bool, error) {
	res := r.db.WithContext(ctx).
		Model(&types.CodeWikiRepository{}).
		Where("id = ? AND status <> ?", id, types.CodeWikiRepoStatusSyncing).
		Updates(map[string]interface{}{
			"status":        types.CodeWikiRepoStatusSyncing,
			"error_message": "",
			"updated_at":    time.Now().UTC(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// --- Snapshots --------------------------------------------------------------

func (r *codeWikiRepository) CreateSnapshot(ctx context.Context, snap *types.CodeWikiSnapshot) error {
	if snap.ID == "" {
		snap.ID = uuid.NewString()
	}
	err := r.db.WithContext(ctx).Create(snap).Error
	if isUniqueViolation(err) {
		// Concurrent sync of the same input: reuse the winner's row.
		if existing, gErr := r.FindReusableSnapshot(ctx, snap.RepositoryID, snap.CommitSHA, snap.FilterHash); gErr == nil {
			*snap = *existing
			return nil
		}
	}
	return err
}

func (r *codeWikiRepository) FindReusableSnapshot(ctx context.Context, repositoryID, commitSHA, filterHash string) (*types.CodeWikiSnapshot, error) {
	var snap types.CodeWikiSnapshot
	err := r.db.WithContext(ctx).
		Where("repository_id = ? AND commit_sha = ? AND filter_hash = ?", repositoryID, commitSHA, filterHash).
		First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiSnapshotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (r *codeWikiRepository) GetSnapshot(ctx context.Context, kbID, id string) (*types.CodeWikiSnapshot, error) {
	var snap types.CodeWikiSnapshot
	err := r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiSnapshotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (r *codeWikiRepository) LatestSnapshot(ctx context.Context, repositoryID string) (*types.CodeWikiSnapshot, error) {
	var snap types.CodeWikiSnapshot
	err := r.db.WithContext(ctx).
		Where("repository_id = ?", repositoryID).
		Order("created_at DESC").First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiSnapshotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (r *codeWikiRepository) ListSnapshotsByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiSnapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []*types.CodeWikiSnapshot
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND id IN ?", kbID, ids).
		Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) DeleteSnapshot(ctx context.Context, kbID, id string) error {
	return r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		Delete(&types.CodeWikiSnapshot{}).Error
}

func (r *codeWikiRepository) DeleteSnapshotsByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Delete(&types.CodeWikiSnapshot{}).Error
}

// --- Analysis artifacts -----------------------------------------------------

func (r *codeWikiRepository) UpsertArtifact(ctx context.Context, artifact *types.CodeWikiAnalysisArtifact) error {
	if artifact.ID == "" {
		artifact.ID = uuid.NewString()
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		// Unique on (snapshot_id, profile_version): re-preparation rewrites.
		Columns: []clause.Column{{Name: "snapshot_id"}, {Name: "profile_version"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"engine_version", "provider_handle", "coverage_summary", "status", "error_message", "updated_at",
		}),
	}).Create(artifact).Error
}

func (r *codeWikiRepository) GetArtifact(ctx context.Context, kbID, id string) (*types.CodeWikiAnalysisArtifact, error) {
	var artifact types.CodeWikiAnalysisArtifact
	err := r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *codeWikiRepository) FindArtifact(ctx context.Context, snapshotID string, profileVersion int) (*types.CodeWikiAnalysisArtifact, error) {
	var artifact types.CodeWikiAnalysisArtifact
	err := r.db.WithContext(ctx).
		Where("snapshot_id = ? AND profile_version = ?", snapshotID, profileVersion).
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *codeWikiRepository) LatestReadyArtifact(ctx context.Context, repositoryID string, profileVersion int) (*types.CodeWikiAnalysisArtifact, error) {
	var artifact types.CodeWikiAnalysisArtifact
	err := r.db.WithContext(ctx).
		Where("repository_id = ? AND profile_version = ? AND status = ?", repositoryID, profileVersion, types.CodeWikiArtifactStatusReady).
		Order("created_at DESC").First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *codeWikiRepository) ListArtifactsByRepository(ctx context.Context, repositoryID string) ([]*types.CodeWikiAnalysisArtifact, error) {
	var out []*types.CodeWikiAnalysisArtifact
	err := r.db.WithContext(ctx).
		Where("repository_id = ?", repositoryID).
		Order("created_at DESC").Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) ListArtifactsByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiAnalysisArtifact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []*types.CodeWikiAnalysisArtifact
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND id IN ?", kbID, ids).Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) DeleteArtifactsByProfileVersion(ctx context.Context, repositoryID string, profileVersion int) error {
	return r.db.WithContext(ctx).
		Where("repository_id = ? AND profile_version = ?", repositoryID, profileVersion).
		Delete(&types.CodeWikiAnalysisArtifact{}).Error
}

func (r *codeWikiRepository) DeleteArtifactsByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Delete(&types.CodeWikiAnalysisArtifact{}).Error
}

// --- Runs -------------------------------------------------------------------

func (r *codeWikiRepository) CreateRun(ctx context.Context, run *types.CodeWikiRun) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	err := r.db.WithContext(ctx).Create(run).Error
	if isUniqueViolation(err) {
		return types.ErrCodeWikiRunActiveExists
	}
	return err
}

func (r *codeWikiRepository) GetRun(ctx context.Context, kbID, id string) (*types.CodeWikiRun, error) {
	var run types.CodeWikiRun
	err := r.db.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ?", id, kbID).
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *codeWikiRepository) GetRunByID(ctx context.Context, id string) (*types.CodeWikiRun, error) {
	var run types.CodeWikiRun
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, types.ErrCodeWikiRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *codeWikiRepository) ListRuns(ctx context.Context, kbID string, limit int) ([]*types.CodeWikiRun, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []*types.CodeWikiRun
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) ActiveRun(ctx context.Context, kbID string) (*types.CodeWikiRun, error) {
	var run types.CodeWikiRun
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND status NOT IN ?", kbID, []string{
			types.CodeWikiRunStatusPublished, types.CodeWikiRunStatusSuperseded,
			types.CodeWikiRunStatusFailed, types.CodeWikiRunStatusCanceled,
		}).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *codeWikiRepository) PublishedRun(ctx context.Context, kbID string) (*types.CodeWikiRun, error) {
	var run types.CodeWikiRun
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND status = ?", kbID, types.CodeWikiRunStatusPublished).
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *codeWikiRepository) UpdateRun(ctx context.Context, run *types.CodeWikiRun) error {
	// Worker snapshots must never clear a pause/cancel request made while a
	// model or evidence call was in flight. Control flags have their own write.
	return r.db.WithContext(ctx).Omit("PauseRequested", "CancelRequested", "LeaseOwner", "LeaseExpiresAt").Save(run).Error
}

func (r *codeWikiRepository) RequestRunControl(ctx context.Context, kbID, runID, control string) error {
	query := r.db.WithContext(ctx).Model(&types.CodeWikiRun{}).
		Where("knowledge_base_id = ? AND id = ?", kbID, runID)
	var updates map[string]interface{}
	switch control {
	case "pause", "cancel":
		query = query.Where("status IN ?", []string{
			types.CodeWikiRunStatusPending, types.CodeWikiRunStatusPreparing,
			types.CodeWikiRunStatusAnalyzing, types.CodeWikiRunStatusPlanning,
			types.CodeWikiRunStatusGenerating, types.CodeWikiRunStatusValidating,
			types.CodeWikiRunStatusPaused,
		})
		updates = map[string]interface{}{control + "_requested": true}
	case "resume":
		query = query.Where("status = ?", types.CodeWikiRunStatusPaused)
		updates = map[string]interface{}{"pause_requested": false}
	default:
		return fmt.Errorf("unsupported run control: %s", control)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("run state changed; refresh before requesting %s", control)
	}
	return nil
}

func (r *codeWikiRepository) UpdateRunPageProgress(ctx context.Context, runID, pageKey, step string) error {
	switch step {
	case "retrieving", "generating", "validating":
	default:
		return fmt.Errorf("unsupported page progress step: %s", step)
	}
	return r.db.WithContext(ctx).Model(&types.CodeWikiRunPage{}).
		Where("run_id = ? AND page_key = ? AND status = ?", runID, pageKey, types.CodeWikiRunPageStatusPending).
		Updates(map[string]interface{}{"validation_status": step, "updated_at": time.Now().UTC()}).Error
}

func (r *codeWikiRepository) RecordRunEvent(ctx context.Context, event *types.CodeWikiRunEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *codeWikiRepository) ListRunEvents(ctx context.Context, kbID, runID string, beforeID int64) ([]*types.CodeWikiRunEvent, error) {
	query := r.db.WithContext(ctx).Where("knowledge_base_id = ? AND run_id = ?", kbID, runID)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var events []*types.CodeWikiRunEvent
	err := query.Order("id DESC").Limit(50).Find(&events).Error
	return events, err
}

// A heartbeat extends the lease without changing UpdatedAt or emitting a
// business event. An obsolete worker cannot renew another owner's lease.
func (r *codeWikiRepository) HeartbeatRun(ctx context.Context, runID, owner string) error {
	return r.db.WithContext(ctx).Model(&types.CodeWikiRun{}).
		Where("id = ? AND lease_owner = ? AND status IN ?", runID, owner, []string{
			types.CodeWikiRunStatusPreparing, types.CodeWikiRunStatusAnalyzing, types.CodeWikiRunStatusPlanning,
			types.CodeWikiRunStatusGenerating, types.CodeWikiRunStatusValidating}).
		UpdateColumn("lease_expires_at", time.Now().UTC().Add(time.Duration(types.CodeWikiRunLeaseTTLSeconds)*time.Second)).Error
}

func (r *codeWikiRepository) ApproveRunImpact(ctx context.Context, kbID, runID, planToken string, config types.JSON) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run types.CodeWikiRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND knowledge_base_id = ?", runID, kbID).First(&run).Error; err != nil {
			return err
		}
		var frozen struct {
			Guard *types.CodeWikiPublicationGuard `json:"publication_guard"`
		}
		if json.Unmarshal(run.ConfigSnapshot, &frozen) != nil || frozen.Guard == nil ||
			!frozen.Guard.ReviewRequired || frozen.Guard.PlanToken != planToken || run.Status != types.CodeWikiRunStatusPaused {
			return fmt.Errorf("impact plan changed or is no longer awaiting review")
		}
		return tx.Model(&types.CodeWikiRun{}).Where("id = ?", runID).
			Updates(map[string]any{"config_snapshot": config, "pause_requested": false, "updated_at": time.Now().UTC()}).Error
	})
}

// TryAcquireRunLease claims a pending or paused (resumed) run for a worker
// atomically. The conditional UPDATE means only one worker can flip the run
// into preparing.
func (r *codeWikiRepository) TryAcquireRunLease(ctx context.Context, runID, owner string, ttlSeconds int) (bool, error) {
	now := time.Now().UTC()
	expires := now.Add(time.Duration(ttlSeconds) * time.Second)
	res := r.db.WithContext(ctx).
		Model(&types.CodeWikiRun{}).
		Where("id = ? AND status IN ?", runID, []string{
			types.CodeWikiRunStatusPending, types.CodeWikiRunStatusPaused,
		}).
		Updates(map[string]interface{}{
			"status":           types.CodeWikiRunStatusPreparing,
			"lease_owner":      owner,
			"lease_expires_at": expires,
			"started_at":       now,
			"updated_at":       now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteRunsByKB soft-deletes a KB's runs. Non-terminal rows are first
// flipped to canceled: the partial unique index (one active run per KB)
// does not include deleted_at, so a soft-deleted "generating" row would
// otherwise keep occupying the index slot.
func (r *codeWikiRepository) DeleteRunsByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("knowledge_base_id = ?", kbID).Delete(&types.CodeWikiFactRecord{}).Error; err != nil {
			return err
		}
		if err := tx.Where("knowledge_base_id = ?", kbID).Delete(&types.CodeWikiRunEvent{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.CodeWikiRun{}).
			Where("knowledge_base_id = ? AND status NOT IN ?", kbID, []string{
				types.CodeWikiRunStatusPublished, types.CodeWikiRunStatusSuperseded,
				types.CodeWikiRunStatusFailed, types.CodeWikiRunStatusCanceled,
			}).
			Updates(map[string]interface{}{
				"status":     types.CodeWikiRunStatusCanceled,
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return err
		}
		return tx.Where("knowledge_base_id = ?", kbID).Delete(&types.CodeWikiRun{}).Error
	})
}

// --- Run manifest -----------------------------------------------------------

func (r *codeWikiRepository) ReplaceRunRepositories(ctx context.Context, runID string, entries []*types.CodeWikiRunRepository) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("run_id = ?", runID).Delete(&types.CodeWikiRunRepository{}).Error; err != nil {
			return err
		}
		for _, entry := range entries {
			entry.ID = uuid.NewString()
			// Stamp the owning run here — the service builds entries before
			// the run row is persisted, and an unstamped RunID silently
			// orphans the manifest: ListRunRepositories then finds nothing
			// and the run generates against empty scopes.
			entry.RunID = runID
			if err := tx.Create(entry).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *codeWikiRepository) ListRunRepositories(ctx context.Context, runID string) ([]*types.CodeWikiRunRepository, error) {
	var out []*types.CodeWikiRunRepository
	err := r.db.WithContext(ctx).
		Where("run_id = ?", runID).Order("created_at ASC").Find(&out).Error
	return out, err
}

// --- Staged pages -----------------------------------------------------------

func (r *codeWikiRepository) UpsertRunPage(ctx context.Context, page *types.CodeWikiRunPage) error {
	if page.ID == "" {
		page.ID = uuid.NewString()
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		// Unique on (run_id, page_key): regeneration replaces the candidate.
		Columns: []clause.Column{{Name: "run_id"}, {Name: "page_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "page_type", "category", "category_path", "sort_order", "content", "summary",
			"evidence", "baseline_version", "baseline_last_edit_source", "scope", "plan_metadata",
			"validation_status", "validation_errors", "status", "generation_error", "updated_at",
		}),
	}).Create(page).Error
}

func (r *codeWikiRepository) ListRunPages(ctx context.Context, runID string) ([]*types.CodeWikiRunPage, error) {
	var out []*types.CodeWikiRunPage
	err := r.db.WithContext(ctx).
		Where("run_id = ?", runID).Order("sort_order ASC, page_key ASC").Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) ListRawWikiPages(ctx context.Context, kbID string) ([]*types.WikiPage, error) {
	var pages []*types.WikiPage
	err := r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).
		Order("sort_order ASC, slug ASC").Find(&pages).Error
	return pages, err
}

func (r *codeWikiRepository) UpdateRunPageStatus(ctx context.Context, page *types.CodeWikiRunPage) error {
	return r.db.WithContext(ctx).
		Model(&types.CodeWikiRunPage{}).
		Where("id = ?", page.ID).
		Updates(map[string]interface{}{
			"status":     page.Status,
			"updated_at": time.Now().UTC(),
		}).Error
}

func (r *codeWikiRepository) DeleteRunPages(ctx context.Context, runID string) error {
	return r.db.WithContext(ctx).
		Where("run_id = ?", runID).Delete(&types.CodeWikiRunPage{}).Error
}

// --- Evidence ---------------------------------------------------------------

func (r *codeWikiRepository) CreateEvidence(ctx context.Context, rows []*types.CodeWikiPageEvidence) error {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if row.ID == "" {
			row.ID = uuid.NewString()
		}
	}
	return r.db.WithContext(ctx).CreateInBatches(rows, 200).Error
}

func (r *codeWikiRepository) ListEvidenceByPageVersion(ctx context.Context, kbID, pageID string, version int) ([]*types.CodeWikiPageEvidence, error) {
	var out []*types.CodeWikiPageEvidence
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND page_id = ? AND page_version = ?", kbID, pageID, version).
		Order("evidence_key ASC").Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) ListEvidenceByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiPageEvidence, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []*types.CodeWikiPageEvidence
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND id IN ?", kbID, ids).Find(&out).Error
	return out, err
}

func (r *codeWikiRepository) DeleteEvidenceByRun(ctx context.Context, runID string) error {
	return r.db.WithContext(ctx).
		Where("run_id = ?", runID).Delete(&types.CodeWikiPageEvidence{}).Error
}

func (r *codeWikiRepository) DeleteEvidenceByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).Delete(&types.CodeWikiPageEvidence{}).Error
}

// Current page versions only: historical revisions cannot appear as current
// entry-to-document links. Old source evidence on a current page is explicit.
func (r *codeWikiRepository) ListSourcePages(ctx context.Context, kbID, repositoryID, snapshotID, path, after string) (*types.CodeWikiSourcePages, error) {
	var pages []types.CodeWikiSourcePage
	err := r.db.WithContext(ctx).Table("wiki_pages AS p").
		Select("p.slug AS page_key, p.title, p.version, MAX(CASE WHEN e.snapshot_id = ? THEN 1 ELSE 0 END) AS matches_snapshot", snapshotID).
		Joins("JOIN code_wiki_page_evidence AS e ON e.page_id = p.id AND e.page_version = p.version AND e.knowledge_base_id = p.knowledge_base_id").
		Where("p.knowledge_base_id = ? AND e.repository_id = ? AND e.path = ? AND p.slug > ? AND p.deleted_at IS NULL AND p.status = ?", kbID, repositoryID, path, after, types.WikiPageStatusPublished).
		Group("p.id, p.slug, p.title, p.version").Order("p.slug ASC").Limit(201).Scan(&pages).Error
	if err != nil {
		return nil, err
	}
	result := &types.CodeWikiSourcePages{Pages: pages}
	if len(pages) > 200 {
		result.Pages = pages[:200]
		result.NextCursor = pages[199].PageKey
	}
	return result, nil
}

// --- Batch publish ----------------------------------------------------------

// PublishRun performs the whole-batch atomic publish. Ordering inside the
// single transaction (see plan §5.4):
//
//  1. re-read each live page and verify the candidate's baseline version —
//     a mismatch means a human edit landed during generation and aborts
//     the publish (conflicts are reported, nothing is written),
//  2. apply the candidate via the caller's applyPage callback (computes
//     the next page state + revision snapshot), insert the revision and
//     upsert the page,
//  3. insert evidence rows bound to the published page versions,
//  4. flip staged pages → published, the run → published, and the previous
//     published run → superseded.
//
// No partial state is ever visible: any error rolls back everything.
func (r *codeWikiRepository) PublishRun(
	ctx context.Context,
	run *types.CodeWikiRun,
	pages []*types.CodeWikiRunPage,
	evidenceByPage map[string][]*types.CodeWikiEvidenceSpec,
	applyPage func(tx interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error),
) ([]string, error) {
	var conflicts []string
	publishErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		conflicts = conflicts[:0]
		handle := txHandle{tx: tx}
		now := time.Now().UTC()

		type published struct {
			candidate *types.CodeWikiRunPage
			page      *types.WikiPage
		}
		var publishedPages []published
		untouchedFingerprints := make(map[string]string)
		writtenSlugs := make(map[string]bool)
		// Re-read both the run and its baseline inside this transaction. A stale
		// caller, duplicate publish or a changed baseline cannot advance history.
		var stored types.CodeWikiRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND knowledge_base_id = ?", run.ID, run.KnowledgeBaseID).First(&stored).Error; err != nil {
			return err
		}
		if stored.Status != types.CodeWikiRunStatusStaged {
			return fmt.Errorf("run is no longer staged")
		}
		var frozen struct {
			Guard *types.CodeWikiPublicationGuard `json:"publication_guard"`
		}
		if len(stored.ConfigSnapshot) > 0 && json.Unmarshal(stored.ConfigSnapshot, &frozen) != nil {
			return fmt.Errorf("invalid publication config")
		}
		if guard := frozen.Guard; guard != nil {
			if guard.ReviewRequired {
				return fmt.Errorf("joint impact plan has not been reviewed")
			}
			var baseline types.CodeWikiRun
			err := tx.Where("knowledge_base_id = ? AND status = ?", run.KnowledgeBaseID,
				types.CodeWikiRunStatusPublished).First(&baseline).Error
			if (err != nil && !errors.Is(err, gorm.ErrRecordNotFound)) || baseline.ID != guard.BaselineRunID {
				return fmt.Errorf("published joint baseline changed; create a new run")
			}
			var livePages []*types.WikiPage
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("knowledge_base_id = ?", run.KnowledgeBaseID).
				Find(&livePages).Error; err != nil {
				return err
			}
			if len(livePages) != len(guard.Pages) {
				return fmt.Errorf("wiki page set changed since generation started")
			}
			for _, page := range livePages {
				untouchedFingerprints[page.Slug] = types.CodeWikiPageFingerprint(page)
				if guard.Pages[page.Slug] != types.CodeWikiPageFingerprint(page) {
					keepHuman := false
					for _, candidate := range pages {
						if candidate.PageKey == page.Slug && candidate.Status == types.CodeWikiRunPageStatusSkipped &&
							page.LastEditSource == types.WikiEditSourceUser && page.Version != candidate.BaselineVersion {
							keepHuman = true
							break
						}
					}
					if keepHuman {
						continue
					}
					return fmt.Errorf("raw baseline changed for page %s; regenerate against the new baseline", page.Slug)
				}
			}
			allowed := make(map[string]bool, len(guard.AllowedPages))
			for _, slug := range guard.AllowedPages {
				if allowed[slug] || slug == "" {
					return fmt.Errorf("invalid publication whitelist")
				}
				allowed[slug] = true
			}
			seen := make(map[string]bool, len(pages))
			for _, page := range pages {
				if page.RunID != run.ID || page.KnowledgeBaseID != run.KnowledgeBaseID ||
					!allowed[page.PageKey] || seen[page.PageKey] {
					return fmt.Errorf("page %s is outside the frozen publication whitelist", page.PageKey)
				}
				seen[page.PageKey] = true
			}
		}

		// Phase 1: baseline verification for every candidate first — no
		// writes happen until all pages pass, so a conflict leaves the
		// previous version fully intact.
		for _, candidate := range pages {
			if candidate.Status != types.CodeWikiRunPageStatusStaged {
				continue
			}
			live, err := handle.GetWikiPageBySlug(run.KnowledgeBaseID, candidate.PageKey)
			if err != nil {
				return fmt.Errorf("read live page %s: %w", candidate.PageKey, err)
			}
			if live == nil {
				if candidate.BaselineVersion != 0 {
					conflicts = append(conflicts, candidate.PageKey)
					continue
				}
				continue
			}
			if live.Version != candidate.BaselineVersion {
				conflicts = append(conflicts, candidate.PageKey)
				continue
			}
			// Human edits are never silently overwritten: a page whose
			// baseline was user-authored is always a conflict, the user
			// must explicitly choose (resolve=keep) at the API level.
		}
		if len(conflicts) > 0 {
			return types.ErrCodeWikiPublishConflict
		}

		// Phase 2: apply pages (revision snapshot + upsert).
		for _, candidate := range pages {
			if candidate.Status != types.CodeWikiRunPageStatusStaged {
				continue
			}
			live, err := handle.GetWikiPageBySlug(run.KnowledgeBaseID, candidate.PageKey)
			if err != nil {
				return err
			}
			if frozen.Guard != nil && live != nil && (candidate.Title != live.Title || candidate.PageType != live.PageType ||
				strings.Join(candidate.CategoryPath, "\x00") != strings.Join(live.CategoryPath, "\x00")) {
				return fmt.Errorf("page %s identity or directory changes require an explicit migration", candidate.PageKey)
			}
			if frozen.Guard != nil && frozen.Guard.ReviewApproved && live != nil {
				if err := types.ValidateCodeWikiSectionChanges(live.Content, candidate.Content, frozen.Guard.AllowedSections[candidate.PageKey]); err != nil {
					return err
				}
			}
			if frozen.Guard != nil && frozen.Guard.ReviewApproved && live == nil {
				identity := frozen.Guard.NewPages[candidate.PageKey]
				if identity == "" || frozen.Guard.AllowedSections[candidate.PageKey]["create"] != identity {
					return fmt.Errorf("new page was not approved in the frozen impact plan: %s", candidate.PageKey)
				}
			}
			// Identical candidates do not create revisions, evidence or writes to
			// the live row. Preserve its timestamps and original evidence version.
			if live != nil && live.Title == candidate.Title && live.PageType == candidate.PageType &&
				live.Content == candidate.Content && live.Summary == candidate.Summary && live.Status == types.WikiPageStatusPublished {
				if err := tx.Model(&types.CodeWikiRunPage{}).Where("id = ?", candidate.ID).
					Update("status", types.CodeWikiRunPageStatusPreserved).Error; err != nil {
					return err
				}
				continue
			}
			var oldLinks types.StringArray
			if live != nil {
				oldLinks = append(oldLinks, live.OutLinks...)
			}
			nextPage, revision, err := applyPage(handle, candidate, live)
			if err != nil {
				return fmt.Errorf("apply page %s: %w", candidate.PageKey, err)
			}
			if frozen.Guard != nil {
				for _, target := range nextPage.OutLinks {
					known := false
					for _, prior := range oldLinks {
						if prior == target {
							known = true
							break
						}
					}
					for _, other := range pages {
						if other.PageKey == target && other.Status == types.CodeWikiRunPageStatusStaged {
							known = true
							break
						}
					}
					if !known {
						linked, err := handle.GetWikiPageBySlug(run.KnowledgeBaseID, target)
						if err != nil {
							return err
						}
						if linked == nil {
							return fmt.Errorf("new dangling wiki link: %s -> %s", candidate.PageKey, target)
						}
					}
				}
			}
			if revision != nil {
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "page_id"}, {Name: "version"}},
					DoNothing: true,
				}).Create(revision).Error; err != nil {
					return err
				}
			}
			if live == nil {
				if err := tx.Create(nextPage).Error; err != nil {
					return err
				}
			} else {
				if err := updateWikiPageRow(tx, nextPage); err != nil {
					return err
				}
			}
			publishedPages = append(publishedPages, published{candidate: candidate, page: nextPage})
			writtenSlugs[candidate.PageKey] = true
		}

		// Phase 3: evidence rows bound to the published versions.
		var evidenceRows []*types.CodeWikiPageEvidence
		for _, pub := range publishedPages {
			for _, spec := range evidenceByPage[pub.candidate.PageKey] {
				evidenceRows = append(evidenceRows, &types.CodeWikiPageEvidence{
					ID:              uuid.NewString(),
					TenantID:        run.TenantID,
					KnowledgeBaseID: run.KnowledgeBaseID,
					PageID:          pub.page.ID,
					PageVersion:     pub.page.Version,
					RunID:           run.ID,
					EvidenceKey:     spec.ID,
					RepositoryID:    spec.RepositoryID,
					SnapshotID:      spec.SnapshotID,
					CommitSHA:       spec.CommitSHA,
					Path:            spec.Path,
					StartLine:       spec.StartLine,
					EndLine:         spec.EndLine,
					Symbol:          spec.Symbol,
					Type:            spec.Type,
					CreatedAt:       now,
				})
			}
		}
		if len(evidenceRows) > 0 {
			if err := tx.CreateInBatches(evidenceRows, 200).Error; err != nil {
				return err
			}
		}
		// Audit the actual resulting live rows before commit, including effects
		// from callbacks/triggers. A protected row may not receive metadata-only
		// changes either. No-op pages remain protected by this second check.
		if frozen.Guard != nil {
			var after []*types.WikiPage
			if err := tx.Where("knowledge_base_id = ?", run.KnowledgeBaseID).Find(&after).Error; err != nil {
				return err
			}
			seen := make(map[string]bool, len(after))
			for _, page := range after {
				seen[page.Slug] = true
				if !writtenSlugs[page.Slug] && untouchedFingerprints[page.Slug] != types.CodeWikiPageFingerprint(page) {
					return fmt.Errorf("actual write audit detected a change outside the publication write set: %s", page.Slug)
				}
			}
			for slug := range untouchedFingerprints {
				if !seen[slug] {
					return fmt.Errorf("actual write audit detected a deleted page: %s", slug)
				}
			}
		}

		// Phase 4: staging statuses + run transitions.
		for _, candidate := range pages {
			if candidate.Status == types.CodeWikiRunPageStatusSkipped || candidate.Status == types.CodeWikiRunPageStatusPreserved {
				if err := tx.Model(&types.CodeWikiRunPage{}).Where("id = ? AND run_id = ?", candidate.ID, run.ID).
					Update("status", candidate.Status).Error; err != nil {
					return err
				}
			}
		}
		for _, pub := range publishedPages {
			if err := tx.Model(&types.CodeWikiRunPage{}).
				Where("id = ?", pub.candidate.ID).
				Updates(map[string]interface{}{"status": types.CodeWikiRunPageStatusPublished, "updated_at": now}).
				Error; err != nil {
				return err
			}
		}
		// Supersede the previous published run inside the same transaction;
		// the partial unique index keeps "one published run per KB" true
		// even under concurrent publishes.
		if err := tx.Model(&types.CodeWikiRun{}).
			Where("knowledge_base_id = ? AND status = ? AND id <> ?",
				run.KnowledgeBaseID, types.CodeWikiRunStatusPublished, run.ID).
			Updates(map[string]interface{}{
				"status":     types.CodeWikiRunStatusSuperseded,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.CodeWikiRun{}).
			Where("id = ?", run.ID).
			Updates(map[string]interface{}{
				"status":       types.CodeWikiRunStatusPublished,
				"published_at": now,
				"finished_at":  now,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		run.Status = types.CodeWikiRunStatusPublished
		run.PublishedAt = &now
		run.FinishedAt = &now
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if errors.Is(publishErr, types.ErrCodeWikiPublishConflict) {
		// Mark conflicting candidates so the UI can show which pages moved.
		for _, slug := range conflicts {
			for _, candidate := range pages {
				if candidate.PageKey == slug {
					candidate.Status = types.CodeWikiRunPageStatusConflict
					_ = r.db.WithContext(ctx).
						Model(&types.CodeWikiRunPage{}).
						Where("id = ?", candidate.ID).
						Update("status", types.CodeWikiRunPageStatusConflict).Error
				}
			}
		}
		return conflicts, types.ErrCodeWikiPublishConflict
	}
	if publishErr != nil {
		logger.Errorf(ctx, "code wiki publish run %s failed: %v", run.ID, publishErr)
		return nil, publishErr
	}
	return nil, nil
}
