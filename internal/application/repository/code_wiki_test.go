package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newCodeWikiTestDB builds an in-memory SQLite database with the code wiki
// schema. GORM AutoMigrate cannot express the partial unique indexes the
// production migrations create, so the behavior-critical ones are created
// by hand here (GORM ignores them; the SQL layer enforces them).
func newCodeWikiTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&types.CodeWikiAnalysisProfile{},
		&types.CodeWikiRepository{},
		&types.CodeWikiSnapshot{},
		&types.CodeWikiAnalysisArtifact{},
		&types.CodeWikiRun{},
		&types.CodeWikiRunEvent{},
		&types.CodeWikiRunRepository{},
		&types.CodeWikiRunPage{},
		&types.CodeWikiPageEvidence{},
		&types.CodeWikiFactRecord{},
		&types.WikiPage{},
		&types.WikiFolder{},
		&types.WikiPageRevision{},
	))
	// Partial unique indexes from migrations/versioned/000092.
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_profiles_kb
		ON code_wiki_analysis_profiles (knowledge_base_id) WHERE deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_repos_unique
		ON code_wiki_repositories (knowledge_base_id, source_id, project_id, ref) WHERE deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_active
		ON code_wiki_runs (knowledge_base_id)
		WHERE status NOT IN ('published', 'superseded', 'failed', 'canceled')`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_published
		ON code_wiki_runs (knowledge_base_id) WHERE status = 'published'`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_run_pages_unique
		ON code_wiki_run_pages (run_id, page_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_artifacts_unique
		ON code_wiki_analysis_artifacts (snapshot_id, profile_version)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_snapshots_reuse
		ON code_wiki_snapshots (repository_id, commit_sha, filter_hash) WHERE deleted_at IS NULL`).Error)
	return db
}

func TestCodeWikiProfileLazyCreationIsIdempotent(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	first, err := repo.GetAnalysisProfile(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiProviderBuiltin, first.ProviderKind)
	require.Equal(t, 1, first.ConfigVersion)

	// Second access returns the same row (no duplicate insert).
	second, err := repo.GetAnalysisProfile(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)

	// A different KB gets its own profile.
	other, err := repo.GetAnalysisProfile(ctx, 7, "kb-2")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, other.ID)
}

func TestCodeWikiProgressDoesNotLoseConcurrentControlRequest(t *testing.T) {
	for _, control := range []string{"pause", "cancel"} {
		t.Run(control, func(t *testing.T) {
			db := newCodeWikiTestDB(t)
			repo := NewCodeWikiRepository(db)
			ctx := context.Background()
			run := &types.CodeWikiRun{KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusGenerating}
			require.NoError(t, repo.CreateRun(ctx, run))
			// The worker holds an old copy across an expensive model call.
			worker, err := repo.GetRunByID(ctx, run.ID)
			require.NoError(t, err)
			require.NoError(t, repo.RequestRunControl(ctx, "kb-1", run.ID, control))
			worker.Phase = "校验代码引用"
			worker.CompletedPages = 1
			require.NoError(t, repo.UpdateRun(ctx, worker))
			stored, err := repo.GetRunByID(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, 1, stored.CompletedPages)
			if control == "pause" {
				require.True(t, stored.PauseRequested)
			} else {
				require.True(t, stored.CancelRequested)
			}
			// Cross-KB and terminal controls must not mutate the run.
			require.Error(t, repo.RequestRunControl(ctx, "other-kb", run.ID, control))
			stored.Status = types.CodeWikiRunStatusPublished
			require.NoError(t, repo.UpdateRun(ctx, stored))
			require.Error(t, repo.RequestRunControl(ctx, "kb-1", run.ID, control))
		})
	}
}

func TestCodeWikiPageProgressPreservesIdentityAndCompletedCandidates(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()
	page := &types.CodeWikiRunPage{ID: "page-1", RunID: "run-1", PageKey: "overview",
		Status: types.CodeWikiRunPageStatusPending, BaselineVersion: 7, Title: "Overview"}
	require.NoError(t, repo.UpsertRunPage(ctx, page))
	for _, step := range []string{"retrieving", "generating", "validating"} {
		require.NoError(t, repo.UpdateRunPageProgress(ctx, "run-1", "overview", step))
		pages, err := repo.ListRunPages(ctx, "run-1")
		require.NoError(t, err)
		require.Len(t, pages, 1)
		require.Equal(t, "page-1", pages[0].ID)
		require.Equal(t, 7, pages[0].BaselineVersion)
		require.Equal(t, step, pages[0].ValidationStatus)
	}
	page.Status, page.ValidationStatus, page.Content = types.CodeWikiRunPageStatusStaged, "passed", "original bytes\n"
	require.NoError(t, repo.UpsertRunPage(ctx, page))
	require.NoError(t, repo.UpdateRunPageProgress(ctx, "run-1", "overview", "generating"))
	pages, err := repo.ListRunPages(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, "passed", pages[0].ValidationStatus)
	require.Equal(t, "original bytes\n", pages[0].Content)
	require.Error(t, repo.UpdateRunPageProgress(ctx, "run-1", "overview", "invented"))
}

func TestCodeWikiRepositoryBindingUnique(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	binding := &types.CodeWikiRepository{
		TenantID: 1, KnowledgeBaseID: "kb-1", SourceID: "ds-1",
		ProjectID: "123", Ref: "main", Role: types.CodeWikiRepoRoleBackend,
	}
	require.NoError(t, repo.CreateRepository(ctx, binding))

	dup := &types.CodeWikiRepository{
		TenantID: 1, KnowledgeBaseID: "kb-1", SourceID: "ds-1",
		ProjectID: "123", Ref: "main",
	}
	err := repo.CreateRepository(ctx, dup)
	require.Error(t, err, "same (kb, source, project, ref) must be rejected")

	// Same project on another ref is a distinct binding.
	other := &types.CodeWikiRepository{
		TenantID: 1, KnowledgeBaseID: "kb-1", SourceID: "ds-1",
		ProjectID: "123", Ref: "release", DisplayName: "x",
	}
	require.NoError(t, repo.CreateRepository(ctx, other))
}

func TestCodeWikiRunSingleActiveConstraint(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	run := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, repo.CreateRun(ctx, run))

	second := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusPending}
	err := repo.CreateRun(ctx, second)
	require.ErrorIs(t, err, types.ErrCodeWikiRunActiveExists)

	// Terminal runs never block: a failed run plus a fresh pending run is fine.
	run.Status = types.CodeWikiRunStatusFailed
	require.NoError(t, repo.UpdateRun(ctx, run))
	third := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, repo.CreateRun(ctx, third))
}

func TestCodeWikiTryAcquireRunLeaseIsExclusive(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	run := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, repo.CreateRun(ctx, run))

	ok, err := repo.TryAcquireRunLease(ctx, run.ID, "worker-a", 60)
	require.NoError(t, err)
	require.True(t, ok)

	// Second worker cannot claim a non-pending run.
	ok, err = repo.TryAcquireRunLease(ctx, run.ID, "worker-b", 60)
	require.NoError(t, err)
	require.False(t, ok)

	leased, err := repo.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "worker-a", leased.LeaseOwner)
	require.Equal(t, types.CodeWikiRunStatusPreparing, leased.Status)
	require.NotNil(t, leased.StartedAt)
}

// publishFixture assembles a staged run with two candidate pages (one new,
// one updating an existing page) and their evidence specs.
type publishFixture struct {
	repo   *codeWikiRepository
	run    *types.CodeWikiRun
	newOne *types.CodeWikiRunPage
	update *types.CodeWikiRunPage
}

func setupPublishFixture(t *testing.T, liveEditedDuringGeneration bool) *publishFixture {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()

	// An existing live page the run will update.
	live := &types.WikiPage{
		ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: "kb-1",
		Slug: "system-architecture", Title: "old title", PageType: types.WikiPageTypeSynthesis,
		Status: types.WikiPageStatusPublished, Content: "old", Version: 4,
		LastEditSource: types.WikiEditSourcePipeline,
	}
	require.NoError(t, db.Create(live).Error)

	run := &types.CodeWikiRun{
		TenantID: 1, KnowledgeBaseID: "kb-1",
		Status: types.CodeWikiRunStatusStaged, ProfileVersion: 2,
	}
	require.NoError(t, repo.CreateRun(ctx, run))

	newOne := &types.CodeWikiRunPage{
		TenantID: 1, KnowledgeBaseID: "kb-1", RunID: run.ID,
		PageKey: "project-overview", Title: "项目概览", PageType: types.WikiPageTypeSynthesis,
		Category: types.CodeWikiPageCategoryOverview, Content: "new content [E1]",
		BaselineVersion: 0, ValidationStatus: "passed", Status: types.CodeWikiRunPageStatusStaged,
	}
	update := &types.CodeWikiRunPage{
		TenantID: 1, KnowledgeBaseID: "kb-1", RunID: run.ID,
		PageKey: "system-architecture", Title: "架构 v2", PageType: types.WikiPageTypeSynthesis,
		Category: types.CodeWikiPageCategoryArchitecture, Content: "updated content [E2]",
		BaselineVersion: 4, BaselineLastEditSource: types.WikiEditSourcePipeline,
		ValidationStatus: "passed", Status: types.CodeWikiRunPageStatusStaged,
	}
	if liveEditedDuringGeneration {
		// A human edit lands between generation and publish: the live page
		// moves past the candidate's baseline.
		require.NoError(t, db.Exec(
			"UPDATE wiki_pages SET version = 5, last_edit_source = ? WHERE id = ?",
			types.WikiEditSourceUser, live.ID).Error)
	}
	require.NoError(t, repo.UpsertRunPage(ctx, newOne))
	require.NoError(t, repo.UpsertRunPage(ctx, update))
	return &publishFixture{repo: repo, run: run, newOne: newOne, update: update}
}

func evidenceFor(pages ...*types.CodeWikiRunPage) map[string][]*types.CodeWikiEvidenceSpec {
	out := make(map[string][]*types.CodeWikiEvidenceSpec)
	for i, page := range pages {
		out[page.PageKey] = []*types.CodeWikiEvidenceSpec{{
			ID:           "E1",
			RepositoryID: "repo-1",
			SnapshotID:   "snap-1",
			CommitSHA:    "abc123",
			Path:         "src/main.go",
			StartLine:    1 + i,
			EndLine:      10 + i,
			Type:         types.CodeWikiEvidenceTypeFile,
		}}
	}
	return out
}

func applyPageSimple(_ interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
	if live == nil {
		return &types.WikiPage{
			ID: uuid.NewString(), TenantID: candidate.TenantID, KnowledgeBaseID: candidate.KnowledgeBaseID,
			Slug: candidate.PageKey, Title: candidate.Title, PageType: candidate.PageType,
			Status: types.WikiPageStatusPublished, Content: candidate.Content,
			Version: 1, LastEditSource: types.WikiEditSourcePipeline,
		}, nil, nil
	}
	rev := &types.WikiPageRevision{
		ID: uuid.NewString(), TenantID: live.TenantID, KnowledgeBaseID: live.KnowledgeBaseID,
		PageID: live.ID, Slug: live.Slug, Version: live.Version,
		Title: live.Title, PageType: live.PageType, Status: live.Status,
		Content: live.Content, Summary: live.Summary, EditSource: live.LastEditSource,
	}
	live.Title = candidate.Title
	live.Content = candidate.Content
	return live, rev, nil
}

func TestCodeWikiPublishRunHappyPath(t *testing.T) {
	f := setupPublishFixture(t, false)
	ctx := context.Background()

	_, err := f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.newOne, f.update}, evidenceFor(f.newOne, f.update), applyPageSimple)
	require.NoError(t, err)

	// Run transitioned to published.
	published, err := f.repo.GetRunByID(ctx, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusPublished, published.Status)
	require.NotNil(t, published.PublishedAt)

	// New page created at version 1 with the candidate content.
	var created types.WikiPage
	require.NoError(t, f.repo.db.Where("slug = ?", "project-overview").First(&created).Error)
	require.Equal(t, 1, created.Version)
	require.Equal(t, "new content [E1]", created.Content)

	// Updated page advanced to version 5; a revision snapshot of v4 exists.
	var updated types.WikiPage
	require.NoError(t, f.repo.db.Where("slug = ?", "system-architecture").First(&updated).Error)
	require.Equal(t, 5, updated.Version)
	require.Equal(t, "架构 v2", updated.Title)
	var rev types.WikiPageRevision
	require.NoError(t, f.repo.db.Where("page_id = ? AND version = ?", updated.ID, 4).First(&rev).Error)
	require.Equal(t, "old title", rev.Title, "revision snapshots the pre-edit state")
	require.Equal(t, "old", rev.Content)

	// Evidence rows are bound to the published versions.
	for page, wantVersion := range map[string]int{"project-overview": 1, "system-architecture": 5} {
		var rows []types.CodeWikiPageEvidence
		require.NoError(t, f.repo.db.
			Where("knowledge_base_id = ? AND page_id = (SELECT id FROM wiki_pages WHERE slug = ?) AND page_version = ?", "kb-1", page, wantVersion).
			Find(&rows).Error)
		require.Len(t, rows, 1, "page %s carries its evidence at version %d", page, wantVersion)
		require.Equal(t, "E1", rows[0].EvidenceKey)
		require.Equal(t, f.run.ID, rows[0].RunID)
	}

	// Staged candidates flipped to published.
	pages, err := f.repo.ListRunPages(ctx, f.run.ID)
	require.NoError(t, err)
	for _, page := range pages {
		require.Equal(t, types.CodeWikiRunPageStatusPublished, page.Status)
	}
}

func TestCodeWikiPublishRunSecondPublicationSupersedesFirst(t *testing.T) {
	f := setupPublishFixture(t, false)
	ctx := context.Background()
	_, err := f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.newOne, f.update}, evidenceFor(f.newOne, f.update), applyPageSimple)
	require.NoError(t, err)

	// Second run publishes for the same KB.
	run2 := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb-1", Status: types.CodeWikiRunStatusStaged}
	require.NoError(t, f.repo.CreateRun(ctx, run2))
	page2 := &types.CodeWikiRunPage{
		TenantID: 1, KnowledgeBaseID: "kb-1", RunID: run2.ID,
		PageKey: "project-overview", Title: "概览 v2", PageType: types.WikiPageTypeSynthesis,
		Category: types.CodeWikiPageCategoryOverview, Content: "v2 [E1]",
		BaselineVersion: 1, ValidationStatus: "passed", Status: types.CodeWikiRunPageStatusStaged,
	}
	require.NoError(t, f.repo.UpsertRunPage(ctx, page2))
	_, err = f.repo.PublishRun(ctx, run2, []*types.CodeWikiRunPage{page2}, evidenceFor(page2), applyPageSimple)
	require.NoError(t, err)

	first, err := f.repo.GetRunByID(ctx, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusSuperseded, first.Status, "previous published run is superseded")

	second, err := f.repo.GetRunByID(ctx, run2.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusPublished, second.Status)

	// The first run's evidence rows stay intact for its historical version.
	var rows []types.CodeWikiPageEvidence
	require.NoError(t, f.repo.db.Where("run_id = ?", f.run.ID).Find(&rows).Error)
	require.Len(t, rows, 2, "superseding a run never rewrites its historical evidence")
}

func TestCodeWikiPublishRunConflictsProtectHumanEdits(t *testing.T) {
	f := setupPublishFixture(t, true)
	ctx := context.Background()

	_, err := f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.newOne, f.update}, evidenceFor(f.newOne, f.update), applyPageSimple)
	require.ErrorIs(t, err, types.ErrCodeWikiPublishConflict)

	// Nothing was written: the run stays staged, the live page keeps the
	// human edit, no evidence rows exist, and the conflicting candidate is
	// flagged for the UI.
	run, err := f.repo.GetRunByID(ctx, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusStaged, run.Status)

	var live types.WikiPage
	require.NoError(t, f.repo.db.Where("slug = ?", "system-architecture").First(&live).Error)
	require.Equal(t, 5, live.Version)
	require.Equal(t, types.WikiEditSourceUser, live.LastEditSource)

	var createdCount int64
	require.NoError(t, f.repo.db.Model(&types.WikiPage{}).Where("slug = ?", "project-overview").Count(&createdCount).Error)
	require.Zero(t, createdCount, "batch publish is all-or-nothing: the new page is NOT created on conflict")

	var evidenceCount int64
	require.NoError(t, f.repo.db.Model(&types.CodeWikiPageEvidence{}).Count(&evidenceCount).Error)
	require.Zero(t, evidenceCount)

	conflicting, err := f.repo.GetRunByID(ctx, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusStaged, conflicting.Status)
	// The conflict marker lands on the candidate row.
	pages, err := f.repo.ListRunPages(ctx, f.run.ID)
	require.NoError(t, err)
	byKey := map[string]*types.CodeWikiRunPage{}
	for _, p := range pages {
		byKey[p.PageKey] = p
	}
	require.Equal(t, types.CodeWikiRunPageStatusConflict, byKey["system-architecture"].Status)
}

func TestCodeWikiArtifactUpsertBySnapshotAndVersion(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	artifact := &types.CodeWikiAnalysisArtifact{
		TenantID: 1, KnowledgeBaseID: "kb-1", RepositoryID: "repo-1",
		SnapshotID: "snap-1", ProfileVersion: 3, Status: types.CodeWikiArtifactStatusReady,
		ProviderHandle: "cw-kb-snap",
	}
	require.NoError(t, repo.UpsertArtifact(ctx, artifact))

	// Same (snapshot, version) re-preparation updates in place.
	artifact.Status = types.CodeWikiArtifactStatusFailed
	artifact.ErrorMessage = "boom"
	require.NoError(t, repo.UpsertArtifact(ctx, artifact))
	var count int64
	require.NoError(t, db.Model(&types.CodeWikiAnalysisArtifact{}).Count(&count).Error)
	require.Equal(t, int64(1), count)

	// A different profile version is a separate artifact.
	v4 := &types.CodeWikiAnalysisArtifact{
		TenantID: 1, KnowledgeBaseID: "kb-1", RepositoryID: "repo-1",
		SnapshotID: "snap-1", ProfileVersion: 4, Status: types.CodeWikiArtifactStatusReady,
	}
	require.NoError(t, repo.UpsertArtifact(ctx, v4))
	require.NoError(t, db.Model(&types.CodeWikiAnalysisArtifact{}).Count(&count).Error)
	require.Equal(t, int64(2), count)

	found, err := repo.FindArtifact(ctx, "snap-1", 3)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiArtifactStatusFailed, found.Status)

	ready, err := repo.LatestReadyArtifact(ctx, "repo-1", 4)
	require.NoError(t, err)
	require.Equal(t, "snap-1", ready.SnapshotID)

	_, err = repo.LatestReadyArtifact(ctx, "repo-1", 3)
	require.ErrorIs(t, err, types.ErrCodeWikiArtifactNotFound, "failed artifacts are not ready")
}

func TestCodeWikiEvidenceQueriesAreKBScoped(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	rows := []*types.CodeWikiPageEvidence{
		{ID: "ev-1", TenantID: 1, KnowledgeBaseID: "kb-1", PageID: "p1", PageVersion: 1, RunID: "r1", Path: "a.go"},
		{ID: "ev-2", TenantID: 1, KnowledgeBaseID: "kb-2", PageID: "p2", PageVersion: 1, RunID: "r2", Path: "b.go"},
	}
	require.NoError(t, repo.CreateEvidence(ctx, rows))

	mine, err := repo.ListEvidenceByIDs(ctx, "kb-1", []string{"ev-1", "ev-2"})
	require.NoError(t, err)
	require.Len(t, mine, 1, "evidence lookup never crosses the KB boundary")
	require.Equal(t, "ev-1", mine[0].ID)
}

func TestCodeWikiSnapshotReuse(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	snap := &types.CodeWikiSnapshot{
		TenantID: 1, KnowledgeBaseID: "kb-1", RepositoryID: "repo-1",
		CommitSHA: "abc", FilterHash: "h1", LocalPath: "/data/1/kb/snap", FileCount: 10,
	}
	require.NoError(t, repo.CreateSnapshot(ctx, snap))

	found, err := repo.FindReusableSnapshot(ctx, "repo-1", "abc", "h1")
	require.NoError(t, err)
	require.Equal(t, snap.ID, found.ID)

	_, err = repo.FindReusableSnapshot(ctx, "repo-1", "def", "h1")
	require.ErrorIs(t, err, types.ErrCodeWikiSnapshotNotFound)

	// Different filters → no reuse.
	_, err = repo.FindReusableSnapshot(ctx, "repo-1", "abc", "h2")
	require.ErrorIs(t, err, types.ErrCodeWikiSnapshotNotFound)
}

// TestCodeWikiCapabilityCheckJSONRoundTrip guards the JSON blob column the
// service reads back after storing it on the profile.
func TestCodeWikiCapabilityCheckJSONRoundTrip(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()

	profile, err := repo.GetAnalysisProfile(ctx, 1, "kb-1")
	require.NoError(t, err)
	report := types.CodeWikiCapabilityReport{
		Connected: true, MeetsMinimum: true,
		Capabilities: []types.CodeWikiCapabilityStatus{
			{Capability: types.CodeWikiCapCodeSearch, Available: true, Source: "provider", Tool: "search"},
		},
	}
	blob, err := json.Marshal(report)
	require.NoError(t, err)
	profile.CapabilityCheck = types.JSON(blob)
	profile.MeetsMinimum = report.MeetsMinimum
	require.NoError(t, repo.SaveAnalysisProfile(ctx, profile))

	reloaded, err := repo.GetAnalysisProfile(ctx, 1, "kb-1")
	require.NoError(t, err)
	require.Equal(t, profile.ConfigVersion, reloaded.ConfigVersion)
	parsed, err := reloaded.ParsedCapabilityCheck()
	require.NoError(t, err)
	require.True(t, parsed.MeetsMinimum)
	require.Len(t, parsed.Capabilities, 1)
}
