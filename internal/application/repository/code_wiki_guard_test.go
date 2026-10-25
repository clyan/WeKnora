package repository

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCodeWikiGuardedPublicationMultiRoundActualWrites(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()
	changed := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: "contract",
		Title: "契约", PageType: types.WikiPageTypeSynthesis, Content: "original\r\n\n", Version: 4,
		Status: types.WikiPageStatusPublished, LastEditSource: types.WikiEditSourcePipeline}
	untouched := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: "unrelated",
		Title: "无关页", Content: "  exact bytes\r\n\n", Version: 7, SortOrder: 23,
		CategoryPath: types.StringArray{"仓库文档", "后端 · B"}, PageMetadata: types.JSON(`{"custom":"keep"}`)}
	require.NoError(t, db.Create(changed).Error)
	require.NoError(t, db.Create(untouched).Error)
	require.NoError(t, db.Exec(`CREATE TABLE page_writes (slug TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER audit_page_updates AFTER UPDATE ON wiki_pages
 BEGIN INSERT INTO page_writes VALUES (NEW.slug); END`).Error)
	untouchedHash := types.CodeWikiPageFingerprint(untouched)
	baselineRun := ""
	for round, content := range []string{"change-1 [E1]\r\n", "change-1 [E1]\r\n", "change-2 [E1]\n\n", "change-2 [E1]\n\n"} {
		raw, err := repo.ListRawWikiPages(ctx, "kb")
		require.NoError(t, err)
		guard := &types.CodeWikiPublicationGuard{BaselineRunID: baselineRun, Pages: map[string]string{}, AllowedPages: []string{"contract"}}
		for _, page := range raw {
			guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
			if page.Slug == "contract" {
				changed = page
			}
		}
		blob, err := json.Marshal(map[string]any{"publication_guard": guard})
		require.NoError(t, err)
		run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusStaged, ConfigSnapshot: types.JSON(blob), TotalPages: 1}
		require.NoError(t, repo.CreateRun(ctx, run))
		candidate := &types.CodeWikiRunPage{KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "contract", Title: changed.Title,
			PageType: changed.PageType, Content: content, BaselineVersion: changed.Version, Status: types.CodeWikiRunPageStatusStaged}
		require.NoError(t, repo.UpsertRunPage(ctx, candidate))
		_, err = repo.PublishRun(ctx, run, []*types.CodeWikiRunPage{candidate}, evidenceFor(candidate), applyPageSimple)
		require.NoError(t, err, "round %d", round)
		baselineRun = run.ID
		preserved, err := txHandle{tx: db}.GetWikiPageBySlug("kb", "unrelated")
		require.NoError(t, err)
		require.Equal(t, untouchedHash, types.CodeWikiPageFingerprint(preserved))
		// A second delivery must not publish the same run twice.
		_, err = repo.PublishRun(ctx, run, []*types.CodeWikiRunPage{candidate}, nil, applyPageSimple)
		require.ErrorContains(t, err, "no longer staged")
	}
	var writes int64
	require.NoError(t, db.Table("page_writes").Count(&writes).Error)
	require.EqualValues(t, 2, writes, "no-op rounds must perform zero live page writes")
	var revisions, evidence int64
	require.NoError(t, db.Model(&types.WikiPageRevision{}).Count(&revisions).Error)
	require.NoError(t, db.Model(&types.CodeWikiPageEvidence{}).Count(&evidence).Error)
	require.EqualValues(t, 2, revisions)
	require.EqualValues(t, 2, evidence)
}

func TestCodeWikiPublicationRejectsLinksToDeclinedNewPages(t *testing.T) {
	f := setupPublishFixture(t, false)
	ctx := context.Background()
	raw, err := f.repo.ListRawWikiPages(ctx, "kb-1")
	require.NoError(t, err)
	guard := &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{f.update.PageKey, f.newOne.PageKey}}
	for _, page := range raw {
		guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
		if page.Slug == f.update.PageKey {
			f.update.Title = page.Title
		}
	}
	blob, err := json.Marshal(map[string]any{"publication_guard": guard})
	require.NoError(t, err)
	f.run.ConfigSnapshot = types.JSON(blob)
	require.NoError(t, f.repo.UpdateRun(ctx, f.run))
	f.newOne.Status = types.CodeWikiRunPageStatusSkipped
	apply := func(tx interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
		page, revision, err := applyPageSimple(tx, candidate, live)
		if page != nil {
			page.OutLinks = types.StringArray{f.newOne.PageKey}
		}
		return page, revision, err
	}
	_, err = f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.update, f.newOne}, nil, apply)
	require.ErrorContains(t, err, "dangling wiki link")
}

func TestCodeWikiGuardRejectsUnversionedEditsAndWhitelistViolations(t *testing.T) {
	for _, mutation := range []string{"raw bytes", "directory", "new page", "outside whitelist", "title"} {
		t.Run(mutation, func(t *testing.T) {
			f := setupPublishFixture(t, false)
			ctx := context.Background()
			raw, err := f.repo.ListRawWikiPages(ctx, "kb-1")
			require.NoError(t, err)
			guard := &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{f.update.PageKey, f.newOne.PageKey}}
			for _, page := range raw {
				guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
			}
			f.update.Title = raw[0].Title
			switch mutation {
			case "raw bytes":
				require.NoError(t, f.repo.db.Model(raw[0]).UpdateColumn("content", "old ").Error)
			case "directory":
				require.NoError(t, f.repo.db.Model(raw[0]).UpdateColumn("sort_order", 10).Error)
			case "new page":
				require.NoError(t, f.repo.db.Create(&types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb-1", Slug: "unexpected"}).Error)
			case "outside whitelist":
				guard.AllowedPages = []string{f.update.PageKey}
			case "title":
				f.update.Title = "model renamed page"
			}
			blob, err := json.Marshal(map[string]any{"publication_guard": guard})
			require.NoError(t, err)
			f.run.ConfigSnapshot = types.JSON(blob)
			require.NoError(t, f.repo.UpdateRun(ctx, f.run))
			_, err = f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.newOne, f.update}, nil, applyPageSimple)
			require.Error(t, err)
			var count int64
			require.NoError(t, f.repo.db.Model(&types.WikiPage{}).Where("slug = ?", f.newOne.PageKey).Count(&count).Error)
			require.Zero(t, count, "failed guard must roll back the entire batch")
		})
	}
}

func TestCodeWikiHeartbeatAndEventPaginationAreIndependent(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()
	run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, repo.CreateRun(ctx, run))
	ok, err := repo.TryAcquireRunLease(ctx, run.ID, "worker", types.CodeWikiRunLeaseTTLSeconds)
	require.NoError(t, err)
	require.True(t, ok)
	worker, err := repo.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.HeartbeatRun(ctx, run.ID, "worker"))
	fresh, err := repo.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, worker.UpdatedAt, fresh.UpdatedAt)
	require.NoError(t, repo.HeartbeatRun(ctx, run.ID, "other-worker"))
	worker.Phase = "still working"
	require.NoError(t, repo.UpdateRun(ctx, worker))
	stored, err := repo.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, fresh.LeaseExpiresAt, stored.LeaseExpiresAt)
	for i := 0; i < 63; i++ {
		require.NoError(t, repo.RecordRunEvent(ctx, &types.CodeWikiRunEvent{KnowledgeBaseID: "kb", RunID: run.ID, Kind: "phase", Message: "step"}))
	}
	first, err := repo.ListRunEvents(ctx, "kb", run.ID, 0)
	require.NoError(t, err)
	require.Len(t, first, 50)
	second, err := repo.ListRunEvents(ctx, "kb", run.ID, first[len(first)-1].ID)
	require.NoError(t, err)
	require.Len(t, second, 13)
	require.Greater(t, first[len(first)-1].ID, second[0].ID)
	other, err := repo.ListRunEvents(ctx, "another-kb", run.ID, 0)
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestCodeWikiActualWriteAuditRollsBackCollateralChangesAndFolders(t *testing.T) {
	f := setupPublishFixture(t, false)
	ctx := context.Background()
	protected := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb-1", Slug: "protected", Content: "exact original bytes", Version: 5}
	require.NoError(t, f.repo.db.Create(protected).Error)
	raw, err := f.repo.ListRawWikiPages(ctx, "kb-1")
	require.NoError(t, err)
	guard := &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{f.newOne.PageKey, f.update.PageKey}}
	for _, page := range raw {
		guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
		if page.Slug == f.update.PageKey {
			f.update.Title = page.Title
		}
	}
	blob, err := json.Marshal(map[string]any{"publication_guard": guard})
	require.NoError(t, err)
	f.run.ConfigSnapshot = types.JSON(blob)
	require.NoError(t, f.repo.UpdateRun(ctx, f.run))
	require.NoError(t, f.repo.db.Exec(`CREATE TRIGGER collateral_change AFTER UPDATE ON wiki_pages
 WHEN NEW.slug = 'system-architecture' BEGIN UPDATE wiki_pages SET content = 'unexpected' WHERE slug = 'protected'; END`).Error)
	apply := func(tx interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
		if _, err := tx.EnsureWikiFolderPath("kb-1", 1, []string{"临时目录"}, 0); err != nil {
			return nil, nil, err
		}
		return applyPageSimple(tx, candidate, live)
	}
	_, err = f.repo.PublishRun(ctx, f.run, []*types.CodeWikiRunPage{f.newOne, f.update}, evidenceFor(f.newOne, f.update), apply)
	require.ErrorContains(t, err, "actual write audit")
	fresh, err := txHandle{tx: f.repo.db}.GetWikiPageBySlug("kb-1", "protected")
	require.NoError(t, err)
	require.Equal(t, "exact original bytes", fresh.Content)
	var folders, revisions int64
	require.NoError(t, f.repo.db.Model(&types.WikiFolder{}).Count(&folders).Error)
	require.NoError(t, f.repo.db.Model(&types.WikiPageRevision{}).Count(&revisions).Error)
	require.Zero(t, folders)
	require.Zero(t, revisions)
}

func TestCodeWikiImpactApprovalRejectsStaleOrForeignPlan(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()
	guard := &types.CodeWikiPublicationGuard{PlanToken: "frozen-plan", ReviewRequired: true}
	blob, err := json.Marshal(map[string]any{"publication_guard": guard})
	require.NoError(t, err)
	run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPaused, ConfigSnapshot: types.JSON(blob)}
	require.NoError(t, repo.CreateRun(ctx, run))
	guard.ReviewRequired = false
	guard.ReviewApproved = true
	approved, err := json.Marshal(map[string]any{"publication_guard": guard})
	require.NoError(t, err)
	require.Error(t, repo.ApproveRunImpact(ctx, "foreign-kb", run.ID, "frozen-plan", types.JSON(approved)))
	require.Error(t, repo.ApproveRunImpact(ctx, "kb", run.ID, "stale-plan", types.JSON(approved)))
	require.NoError(t, repo.ApproveRunImpact(ctx, "kb", run.ID, "frozen-plan", types.JSON(approved)))
	require.Error(t, repo.ApproveRunImpact(ctx, "kb", run.ID, "frozen-plan", types.JSON(approved)))
}

func TestCodeWikiSQLiteIncrementalMigrationRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE code_wiki_run_pages (id TEXT PRIMARY KEY)").Error)
	up, err := os.ReadFile("../../../migrations/sqlite/000016_code_wiki_activity.up.sql")
	require.NoError(t, err)
	down, err := os.ReadFile("../../../migrations/sqlite/000016_code_wiki_activity.down.sql")
	require.NoError(t, err)
	for round := 0; round < 2; round++ {
		require.NoError(t, db.Exec(string(up)).Error)
		require.True(t, db.Migrator().HasColumn(&types.CodeWikiRunPage{}, "SortOrder"))
		require.True(t, db.Migrator().HasColumn(&types.CodeWikiRunPage{}, "PlanMetadata"))
		require.NoError(t, db.Exec("INSERT INTO code_wiki_run_events (knowledge_base_id, run_id, kind, message) VALUES ('kb', 'run', 'phase', 'step')").Error)
		require.NoError(t, db.Exec(string(down)).Error)
		require.False(t, db.Migrator().HasTable(&types.CodeWikiRunEvent{}))
	}
}
