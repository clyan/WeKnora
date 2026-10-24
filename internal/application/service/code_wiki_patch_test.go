package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type codeWikiPatchKB struct {
	interfaces.KnowledgeBaseService
}

func TestCodeWikiWorkerContextCancellationIsNotUserCancellation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}, &types.CodeWikiRunEvent{}))
	repo := repository.NewCodeWikiRepository(db)
	run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusGenerating}
	require.NoError(t, repo.CreateRun(context.Background(), run))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &codeWikiService{repo: repo}
	s.finalizeRunOnError(ctx, run)
	stored, err := repo.GetRunByID(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusFailed, stored.Status)
	require.Contains(t, stored.ErrorMessage, "worker stopped")
	events, err := repo.ListRunEvents(context.Background(), "kb", run.ID, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestCodeWikiInitialGenerationRejectsUnsupportedClaimsWithoutEditingCandidate(t *testing.T) {
	for _, response := range []string{"supported [E1] but invented [E999]", "no source citations", "supported [E1]"} {
		t.Run(response, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}, &types.CodeWikiRunPage{}, &types.CodeWikiRunEvent{}))
			require.NoError(t, db.Exec("CREATE UNIQUE INDEX initial_candidates ON code_wiki_run_pages(run_id,page_key)").Error)
			repo := repository.NewCodeWikiRepository(db)
			s := &codeWikiService{repo: repo}
			run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusGenerating}
			require.NoError(t, repo.CreateRun(context.Background(), run))
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("actual source"), 0600))
			state := &codeWikiRunState{run: run, cfg: codeWikiRunConfig{MaxModelCalls: 1}, provider: &codeWikiPatchProvider{}, chat: &codeWikiPatchChat{response: response}, repoNames: map[string]string{"frontend": "前端"}, scopes: map[string]codebase.SnapshotScope{"frontend": {RepositoryID: "frontend", SnapshotID: "target", CommitSHA: "sha", LocalPath: root}}}
			page := codeWikiPlannedPage{Slug: "page", Title: "文档", Category: types.CodeWikiPageCategoryRepository}
			require.NoError(t, s.generateOnePage(context.Background(), state, page, nil, "Chinese"))
			rows, err := repo.ListRunPages(context.Background(), run.ID)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, response, rows[0].Content, "failed citations must not silently alter candidate text")
			if response == "supported [E1]" {
				require.Equal(t, "passed", rows[0].ValidationStatus)
			} else {
				require.Equal(t, "failed", rows[0].ValidationStatus)
				require.Zero(t, run.CompletedPages)
			}
		})
	}
}

func (codeWikiPatchKB) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: 1, Type: types.KnowledgeBaseTypeCodeWiki}, nil
}

type codeWikiUnusedChat interface{ chat.Chat }
type codeWikiPatchChat struct {
	codeWikiUnusedChat
	calls    int
	response string
}

func (m *codeWikiPatchChat) Chat(_ context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.calls++
	return &types.ChatResponse{Content: m.response}, nil
}

type codeWikiPatchProvider struct {
	codebase.CodeAnalysisProvider
	searches int
}

func (p *codeWikiPatchProvider) SearchCode(_ context.Context, scope codebase.SnapshotScope, _ string, _ codebase.QueryOptions) ([]codebase.SearchHit, error) {
	p.searches++
	path := "api.ts"
	if scope.RepositoryID == "backend" {
		path = "handler.go"
	}
	return []codebase.SearchHit{{Path: path, StartLine: 1, EndLine: 1, Snippet: "verified source"}}, nil
}
func (*codeWikiPatchProvider) ReadSource(_ context.Context, scope codebase.SnapshotScope, path string, _, _ int) (string, error) {
	data, err := os.ReadFile(filepath.Join(scope.LocalPath, path))
	return string(data), err
}

func TestCodeWikiSectionGenerationAndPublicationReplayPreservesVersionsAndEvidence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WikiPage{}, &types.WikiPageRevision{}, &types.WikiFolder{}, &types.CodeWikiRun{},
		&types.CodeWikiRunPage{}, &types.CodeWikiPageEvidence{}, &types.CodeWikiRunEvent{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX replay_candidates ON code_wiki_run_pages(run_id,page_key)").Error)
	repo := repository.NewCodeWikiRepository(db)
	s := &codeWikiService{repo: repo, kbService: codeWikiPatchKB{}}
	ctx := context.Background()
	live := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", TenantID: 1, Slug: "contract", Title: "跨端契约",
		PageType: types.WikiPageTypeSynthesis, Status: types.WikiPageStatusPublished, CategoryPath: types.StringArray{"接口与跨端契约"},
		Version: 2, LastEditSource: types.WikiEditSourcePipeline, Content: "# 契约\r\n\r\n## 实现\r\nold [E6]\r\n\r\n## 约束\r\n  keep [E6] \r\n\r\n"}
	require.NoError(t, db.Create(live).Error)
	require.NoError(t, repo.CreateEvidence(ctx, []*types.CodeWikiPageEvidence{{ID: uuid.NewString(), KnowledgeBaseID: "kb", PageID: live.ID,
		PageVersion: 2, RunID: "original", EvidenceKey: "E6", RepositoryID: "frontend", SnapshotID: "F0", CommitSHA: "front-old", Path: "api.ts", StartLine: 1, EndLine: 1}}))
	previous := &types.CodeWikiRun{ID: "original", KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPublished, ConfigSnapshot: types.JSON(`{}`)}
	require.NoError(t, repo.CreateRun(ctx, previous))
	model := &codeWikiPatchChat{}
	provider := &codeWikiPatchProvider{}
	publishedHash := ""
	for round := 0; round < 3; round++ {
		raw, err := repo.ListRawWikiPages(ctx, "kb")
		require.NoError(t, err)
		live = raw[0]
		sections, err := types.CodeWikiSections(live.Content)
		require.NoError(t, err)
		guard := &types.CodeWikiPublicationGuard{BaselineRunID: previous.ID, ReviewApproved: true,
			Pages: map[string]string{live.Slug: types.CodeWikiPageFingerprint(live)}, AllowedPages: []string{live.Slug},
			AllowedSections: map[string]map[string]string{}}
		if round != 1 {
			guard.AllowedSections[live.Slug] = map[string]string{sections[1].ID: sections[1].Hash}
		}
		cfg := codeWikiRunConfig{DirectoryVersion: 1, MaxModelCalls: 5, PublicationGuard: guard}
		blob, err := json.Marshal(cfg)
		require.NoError(t, err)
		run := &types.CodeWikiRun{KnowledgeBaseID: "kb", TenantID: 1, Status: types.CodeWikiRunStatusGenerating, ConfigSnapshot: types.JSON(blob), TotalPages: 1}
		require.NoError(t, repo.CreateRun(ctx, run))
		page := codeWikiPlannedPage{Slug: live.Slug, Title: live.Title, Category: types.CodeWikiPageCategoryInteraction,
			CategoryPath: live.CategoryPath, PageType: live.PageType, BaselineVersion: live.Version, Scope: "API contract"}
		require.NoError(t, s.persistPlannedPages(ctx, run, []codeWikiPlannedPage{page}))
		state := &codeWikiRunState{run: run, cfg: cfg, provider: provider, chat: model, repoNames: map[string]string{}, scopes: map[string]codebase.SnapshotScope{}}
		for _, id := range []string{"frontend", "backend"} {
			root := t.TempDir()
			path := "api.ts"
			if id == "backend" {
				path = "handler.go"
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte("changed source\n"), 0600))
			state.scopes[id] = codebase.SnapshotScope{KnowledgeBaseID: "kb", RepositoryID: id, SnapshotID: id + "-target", CommitSHA: id + "-sha", LocalPath: root}
		}
		beforeCalls, beforeSearches := model.calls, provider.searches
		if round == 1 {
			require.NoError(t, s.preserveCodeWikiPage(ctx, state, page))
			run.CompletedPages++
			require.Equal(t, beforeCalls, model.calls)
			require.Equal(t, beforeSearches, provider.searches)
		} else {
			patches := []types.CodeWikiSectionPatch{}
			if round == 0 {
				patches = append(patches, types.CodeWikiSectionPatch{SectionID: sections[1].ID, ExpectedHash: sections[1].Hash,
					Replacement: "## 实现\r\nnew verified backend behavior [E7]\r\n\r\n"})
			}
			response, err := json.Marshal(map[string]any{"patches": patches})
			require.NoError(t, err)
			model.response = string(response)
			require.NoError(t, s.generateOnePage(ctx, state, page, nil, "中文"))
			require.Equal(t, beforeCalls+1, model.calls)
			require.Zero(t, run.FailedPages)
		}
		run.Status = types.CodeWikiRunStatusStaged
		require.NoError(t, repo.UpdateRun(ctx, run))
		_, err = s.PublishRun(ctx, "kb", run.ID, &types.CodeWikiRunPublishRequest{})
		require.NoError(t, err)
		after, err := repo.ListRawWikiPages(ctx, "kb")
		require.NoError(t, err)
		require.Equal(t, 3, after[0].Version)
		require.Contains(t, after[0].Content, "## 约束\r\n  keep [E6] \r\n\r\n")
		if round == 0 {
			publishedHash = types.CodeWikiPageFingerprint(after[0])
		} else {
			require.Equal(t, publishedHash, types.CodeWikiPageFingerprint(after[0]))
		}
		previous = run
	}
	var revisions int64
	require.NoError(t, db.Model(&types.WikiPageRevision{}).Count(&revisions).Error)
	require.EqualValues(t, 1, revisions)
	historical, err := repo.ListEvidenceByPageVersion(ctx, "kb", live.ID, 2)
	require.NoError(t, err)
	require.Len(t, historical, 1)
	require.Equal(t, "F0", historical[0].SnapshotID)
	current, err := repo.ListEvidenceByPageVersion(ctx, "kb", live.ID, 3)
	require.NoError(t, err)
	require.Len(t, current, 2)
	byKey := map[string]*types.CodeWikiPageEvidence{}
	for _, evidence := range current {
		byKey[evidence.EvidenceKey] = evidence
	}
	require.Equal(t, "F0", byKey["E6"].SnapshotID)
	require.Equal(t, "backend-target", byKey["E7"].SnapshotID)
	require.Equal(t, 2, model.calls)
}
