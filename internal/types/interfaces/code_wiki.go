package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// CodeWikiRepository defines persistence for the code wiki domain:
// analysis profiles, repository bindings, immutable snapshots,
// provider-versioned analysis artifacts, generation runs, staged pages and
// published evidence. Every query carries tenant + knowledge base scope;
// cross-KB access is a bug, not a supported use case.
type CodeWikiRepository interface {
	// --- Analysis profiles ---

	// GetAnalysisProfile returns the KB's profile, creating the default
	// builtin profile on first access. tenantID is passed explicitly (not
	// read from context) because first access inserts a row whose NOT NULL
	// tenant column needs a value — a background task without a tenant
	// context must fail with an error, not panic.
	GetAnalysisProfile(ctx context.Context, tenantID uint64, kbID string) (*types.CodeWikiAnalysisProfile, error)
	// SaveAnalysisProfile persists the profile, incrementing ConfigVersion.
	SaveAnalysisProfile(ctx context.Context, profile *types.CodeWikiAnalysisProfile) error

	// --- Repository bindings ---

	// CreateRepository inserts a new binding. The unique index on
	// (kb, source, project, ref) makes duplicates fail.
	CreateRepository(ctx context.Context, repo *types.CodeWikiRepository) error
	// UpdateRepository rewrites mutable binding fields (display name, role,
	// ref, filters, enabled). Changing ref/filters requires a new snapshot
	// on next sync; callers surface that in the UI.
	UpdateRepository(ctx context.Context, repo *types.CodeWikiRepository) error
	// GetRepository resolves a binding by id within a KB.
	GetRepository(ctx context.Context, kbID, id string) (*types.CodeWikiRepository, error)
	// ListRepositories returns bindings matching the filter.
	ListRepositories(ctx context.Context, filter types.CodeWikiRepositoryFilter) ([]*types.CodeWikiRepository, error)
	// DeleteRepository soft-deletes a binding.
	DeleteRepository(ctx context.Context, kbID, id string) error
	// CountRepositories returns the number of live bindings for a KB.
	CountRepositories(ctx context.Context, kbID string) (int64, error)
	// UpdateRepositoryStatus persists the sync summary fields (status,
	// last snapshot/commit, error message) without touching filters.
	UpdateRepositoryStatus(ctx context.Context, repo *types.CodeWikiRepository) error
	// TryMarkSyncing transitions a binding into syncing only when it is not
	// already syncing (compare-and-swap against concurrent submissions).
	TryMarkSyncing(ctx context.Context, id string) (bool, error)

	// --- Snapshots ---

	// CreateSnapshot inserts a new immutable snapshot row.
	CreateSnapshot(ctx context.Context, snap *types.CodeWikiSnapshot) error
	// FindReusableSnapshot returns the live snapshot matching
	// (repository, commit, filter hash) so re-syncs skip re-downloading.
	FindReusableSnapshot(ctx context.Context, repositoryID, commitSHA, filterHash string) (*types.CodeWikiSnapshot, error)
	// GetSnapshot resolves a snapshot by id within a KB.
	GetSnapshot(ctx context.Context, kbID, id string) (*types.CodeWikiSnapshot, error)
	// LatestSnapshot returns the newest snapshot for a repository binding,
	// or ErrCodeWikiSnapshotNotFound.
	LatestSnapshot(ctx context.Context, repositoryID string) (*types.CodeWikiSnapshot, error)
	// ListSnapshotsByIDs resolves a set of snapshot ids in one query.
	ListSnapshotsByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiSnapshot, error)
	// DeleteSnapshot soft-deletes a snapshot row (disk cleanup is the
	// service's job).
	DeleteSnapshot(ctx context.Context, kbID, id string) error
	// DeleteSnapshotsByKB soft-deletes every snapshot of a KB.
	DeleteSnapshotsByKB(ctx context.Context, kbID string) error

	// --- Analysis artifacts ---

	// UpsertArtifact inserts or updates the artifact for
	// (snapshot, profile version).
	UpsertArtifact(ctx context.Context, artifact *types.CodeWikiAnalysisArtifact) error
	// GetArtifact resolves an artifact by id within a KB.
	GetArtifact(ctx context.Context, kbID, id string) (*types.CodeWikiAnalysisArtifact, error)
	// FindArtifact returns the artifact for (snapshot, profile version).
	FindArtifact(ctx context.Context, snapshotID string, profileVersion int) (*types.CodeWikiAnalysisArtifact, error)
	// LatestReadyArtifact returns the newest ready artifact for a
	// repository under one profile version.
	LatestReadyArtifact(ctx context.Context, repositoryID string, profileVersion int) (*types.CodeWikiAnalysisArtifact, error)
	// ListArtifactsByRepository returns artifacts for a repository.
	ListArtifactsByRepository(ctx context.Context, repositoryID string) ([]*types.CodeWikiAnalysisArtifact, error)
	// ListArtifactsByIDs resolves a set of artifact ids within a KB.
	ListArtifactsByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiAnalysisArtifact, error)
	// DeleteArtifactsByProfileVersion removes artifacts for a (repository,
	// profile version) pair — only artifacts this system created.
	DeleteArtifactsByProfileVersion(ctx context.Context, repositoryID string, profileVersion int) error
	// DeleteArtifactsByKB soft-deletes every artifact of a KB.
	DeleteArtifactsByKB(ctx context.Context, kbID string) error

	// --- Runs ---

	// CreateRun inserts a new run. The partial unique index guarantees at
	// most one active run per KB; violations surface as
	// ErrCodeWikiRunActiveExists.
	CreateRun(ctx context.Context, run *types.CodeWikiRun) error
	// GetRun resolves a run by id within a KB.
	GetRun(ctx context.Context, kbID, id string) (*types.CodeWikiRun, error)
	// GetRunByID resolves a run by id alone (task worker path, which has
	// already authorized the KB via the payload).
	GetRunByID(ctx context.Context, id string) (*types.CodeWikiRun, error)
	// ListRuns returns runs for a KB newest-first, capped at limit.
	ListRuns(ctx context.Context, kbID string, limit int) ([]*types.CodeWikiRun, error)
	// ActiveRun returns the non-terminal run for a KB if one exists.
	ActiveRun(ctx context.Context, kbID string) (*types.CodeWikiRun, error)
	// PublishedRun returns the currently published run for a KB if any.
	PublishedRun(ctx context.Context, kbID string) (*types.CodeWikiRun, error)
	// UpdateRun persists progress/status transitions. Callers re-read the
	// run before deciding transitions; the repo does not version-check.
	UpdateRun(ctx context.Context, run *types.CodeWikiRun) error
	// RequestRunControl changes only control flags, without writing a stale
	// worker status/counters. Resume clears the pause flag on a paused run.
	RequestRunControl(ctx context.Context, kbID, runID, control string) error
	RecordRunEvent(ctx context.Context, event *types.CodeWikiRunEvent) error
	ListRunEvents(ctx context.Context, kbID, runID string, beforeID int64) ([]*types.CodeWikiRunEvent, error)
	HeartbeatRun(ctx context.Context, runID, owner string) error
	ApproveRunImpact(ctx context.Context, kbID, runID, planToken string, config types.JSON) error
	// TryAcquireRunLease atomically claims a pending run for a worker:
	// succeeds only when status is still pending (or the previous lease
	// expired). Returns false when another worker owns the run.
	TryAcquireRunLease(ctx context.Context, runID, owner string, ttlSeconds int) (bool, error)
	// DeleteRunsByKB soft-deletes every run of a KB (KB deletion).
	DeleteRunsByKB(ctx context.Context, kbID string) error

	// --- Run manifest ---

	// ReplaceRunRepositories freezes the run's repository/snapshot/artifact
	// manifest in one write (delete + insert inside a transaction).
	ReplaceRunRepositories(ctx context.Context, runID string, entries []*types.CodeWikiRunRepository) error
	// ListRunRepositories returns the frozen manifest for a run.
	ListRunRepositories(ctx context.Context, runID string) ([]*types.CodeWikiRunRepository, error)

	// --- Staged pages ---

	// UpsertRunPage inserts or replaces one staged candidate page.
	UpsertRunPage(ctx context.Context, page *types.CodeWikiRunPage) error
	// UpdateRunPageProgress records the current substep on an existing pending
	// page without replacing its identity, baseline, or candidate content.
	UpdateRunPageProgress(ctx context.Context, runID, pageKey, step string) error
	// ListRunPages returns the staged pages for a run.
	ListRunPages(ctx context.Context, runID string) ([]*types.CodeWikiRunPage, error)
	// ListRawWikiPages returns exact stored rows for freezing publication guards.
	ListRawWikiPages(ctx context.Context, kbID string) ([]*types.WikiPage, error)
	// UpdateRunPageStatus persists staging status transitions
	// (staged → published/conflict/skipped).
	UpdateRunPageStatus(ctx context.Context, page *types.CodeWikiRunPage) error
	// DeleteRunPages removes staged candidates (retry prep).
	DeleteRunPages(ctx context.Context, runID string) error

	// --- Evidence ---

	// CreateEvidence inserts published evidence rows bound to page versions.
	CreateEvidence(ctx context.Context, rows []*types.CodeWikiPageEvidence) error
	// ListEvidenceByPageVersion returns citations for one page version.
	ListEvidenceByPageVersion(ctx context.Context, kbID, pageID string, version int) ([]*types.CodeWikiPageEvidence, error)
	ListSourcePages(ctx context.Context, kbID, repositoryID, snapshotID, path, after string) (*types.CodeWikiSourcePages, error)
	// ListEvidenceByIDs resolves evidence rows by id within a KB.
	ListEvidenceByIDs(ctx context.Context, kbID string, ids []string) ([]*types.CodeWikiPageEvidence, error)
	// DeleteEvidenceByRun removes a run's evidence (unpublished/canceled runs).
	DeleteEvidenceByRun(ctx context.Context, runID string) error
	// DeleteEvidenceByKB hard-deletes a KB's evidence rows (KB deletion).
	DeleteEvidenceByKB(ctx context.Context, kbID string) error

	// --- Batch publish ---

	// PublishRun atomically publishes a staged run inside one transaction:
	//   1. verifies every staged page's baseline against the live page
	//      (conflicts reported back, publish aborted),
	//   2. upserts wiki pages (+ revision snapshots for superseded versions),
	//   3. inserts evidence rows bound to the new page versions,
	//   4. transitions the run to published and supersedes the previous
	//      published run.
	// applyPage is called per page inside the transaction to compute the
	// next wiki page state (new or updated) from the candidate.
	PublishRun(
		ctx context.Context,
		run *types.CodeWikiRun,
		pages []*types.CodeWikiRunPage,
		evidenceByPage map[string][]*types.CodeWikiEvidenceSpec,
		applyPage func(tx TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error),
	) ([]string, error)
}

// TX is the narrow transaction handle passed into PublishRun callbacks so
// callers can run scoped reads without importing gorm into interfaces.
type TX interface {
	// GetWikiPageBySlug reads a live wiki page within the transaction.
	GetWikiPageBySlug(kbID, slug string) (*types.WikiPage, error)
	EnsureWikiFolderPath(kbID string, tenantID uint64, path []string, sortOrder int) (string, error)
}

// CodeWikiService defines the code wiki application service: analysis
// provider profiles, repository binding management, snapshot sync,
// generation runs and publication.
type CodeWikiService interface {
	ApproveRunImpact(ctx context.Context, kbID, runID string, req *types.CodeWikiImpactReviewRequest) error
	ListSourceDirectory(ctx context.Context, kbID, repositoryID, snapshotID, prefix, after string) (*types.CodeWikiSourceDirectory, error)
	ListSourcePages(ctx context.Context, kbID, repositoryID, snapshotID, path, after string) (*types.CodeWikiSourcePages, error)
	ListRunEvents(ctx context.Context, kbID, runID string, beforeID int64) ([]*types.CodeWikiRunEvent, error)
	// --- Analysis provider profile ---

	// GetAnalysisProfile returns the KB's analysis profile.
	GetAnalysisProfile(ctx context.Context, kbID string) (*types.CodeWikiAnalysisProfile, error)
	// UpdateAnalysisProfile validates and saves the provider config
	// (provider kind, MCP service reference, tool mappings, snapshot
	// delivery). Saving does NOT mark it usable — run TestAnalysisProfile.
	UpdateAnalysisProfile(ctx context.Context, kbID string, req *types.CodeWikiAnalysisProfileRequest) (*types.CodeWikiAnalysisProfile, error)
	// TestAnalysisProfile runs the layered capability check (connection →
	// mapping → snapshot readiness → minimum capability) and persists the
	// report. Returns the updated profile.
	TestAnalysisProfile(ctx context.Context, kbID string) (*types.CodeWikiAnalysisProfile, error)

	// --- Repository bindings ---

	ListRepositories(ctx context.Context, kbID string) ([]*types.CodeWikiRepository, error)
	CreateRepository(ctx context.Context, kbID string, req *types.CodeWikiRepositoryUpsertRequest) (*types.CodeWikiRepository, error)
	UpdateRepository(ctx context.Context, kbID, id string, req *types.CodeWikiRepositoryUpsertRequest) (*types.CodeWikiRepository, error)
	DeleteRepository(ctx context.Context, kbID, id string) error

	// --- Sync ---

	// SyncRepositories enqueues snapshot sync for the named bindings (or
	// every enabled binding when ids is empty). Returns the count enqueued.
	SyncRepositories(ctx context.Context, kbID string, ids []string) (int, error)
	// RouteDataSourceSync branches a datasource:sync task for a code_wiki KB:
	// it resolves the bindings under that GitLab data source, marks them
	// syncing and enqueues the snapshot task. The data source sync log's
	// lifecycle is finalized by the code wiki sync handler.
	RouteDataSourceSync(ctx context.Context, ds *types.DataSource, syncLogID string) error
	// ProcessCodeWikiSync is the asynq task handler backing sync.
	ProcessCodeWikiSync(ctx context.Context, task *asynq.Task) error

	// --- Generation runs ---

	CreateRun(ctx context.Context, kbID string, req *types.CodeWikiRunCreateRequest) (*types.CodeWikiRun, error)
	GetRun(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, error)
	// GetRunDetail returns the run plus its frozen repository manifest and
	// (for staged/finished runs) the staged pages — the actual scope, with
	// exclusions and reasons, never a silent subset.
	GetRunDetail(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, []*types.CodeWikiRunRepository, []*types.CodeWikiRunPage, error)
	ListRuns(ctx context.Context, kbID string) ([]*types.CodeWikiRun, error)
	CancelRun(ctx context.Context, kbID, runID string) error
	// PauseRun parks an in-flight run in paused; ResumeRun continues it from
	// the persisted plan (staged pages kept, pending pages regenerate).
	PauseRun(ctx context.Context, kbID, runID string) error
	ResumeRun(ctx context.Context, kbID, runID string) error
	RetryRun(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, error)
	PublishRun(ctx context.Context, kbID, runID string, req *types.CodeWikiRunPublishRequest) (*types.CodeWikiRun, error)
	// ProcessCodeWikiGenerate is the asynq task handler backing generation.
	ProcessCodeWikiGenerate(ctx context.Context, task *asynq.Task) error

	// --- Evidence ---

	// GetEvidence resolves one evidence row by id (authorized via KB) and
	// enriches it with repository/snapshot display fields and the cited
	// source excerpt read from the snapshot on disk.
	GetEvidence(ctx context.Context, kbID, evidenceID string) (*types.CodeWikiPageEvidenceView, error)
	// ListPageEvidence returns the citations bound to one published page
	// version (the wiki browser's source sidebar).
	ListPageEvidence(ctx context.Context, kbID, pageID string, version int) ([]*types.CodeWikiPageEvidenceView, error)

	// --- Lifecycle ---

	// CleanupKnowledgeBase removes ALL code wiki state for a deleted KB:
	// cancels active runs (blocking later publish), soft-deletes bindings,
	// snapshots, artifacts, runs and staged pages, hard-deletes evidence
	// rows, and removes the KB's snapshot directory tree. Shared MCP
	// services and externally owned indexes are never touched. Errors are
	// best-effort and logged — KB deletion must not fail because a
	// provider or the disk is unavailable.
	CleanupKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) error
}
