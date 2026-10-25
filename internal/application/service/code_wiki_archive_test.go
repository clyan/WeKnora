package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiArchiveRequiresConfirmedCompleteDeletion(t *testing.T) {
	page := codeWikiPlannedPage{RepositoryID: "front", SourcePaths: []string{"screen.tsx", "entry.ts"}}
	deleted := []types.CodeWikiSourceChange{
		{RepositoryID: "front", Path: "screen.tsx", Kind: "deleted", BeforeHash: "a"},
		{RepositoryID: "front", Path: "entry.ts", Kind: "deleted", BeforeHash: "b"},
	}
	require.True(t, codeWikiSourcesDeleted(page, deleted))
	require.False(t, codeWikiSourcesDeleted(page, deleted[:1]), "partial removal must reanalyze the remaining source")
	for _, extra := range []types.CodeWikiSourceChange{
		{RepositoryID: "front", Path: "replacement.tsx", Kind: "added", AfterHash: "c"},
		{RepositoryID: "front", Path: "filter", ScopeChanged: true},
	} {
		require.False(t, codeWikiSourcesDeleted(page, append(append([]types.CodeWikiSourceChange(nil), deleted...), extra)))
	}
	moved := page
	moved.SourcePaths = []string{"new.tsx", "entry.ts"}
	require.False(t, codeWikiSourcesDeleted(moved, deleted))
	removeDeletedCodeWikiRoots(&page, deleted[:1])
	require.Equal(t, []string{"entry.ts"}, page.SourcePaths)
	require.False(t, codeWikiSourcesDeleted(codeWikiPlannedPage{}, deleted))
}

func TestCodeWikiDeletionStagesArchiveAndEmptyGuideWithoutModelOrLiveWrite(t *testing.T) {
	s, db, baseline, _ := topicServiceFixture(t)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_archive_page_identity ON code_wiki_run_pages(run_id, page_key)`).Error)
	ctx := context.Background()
	baseline.Status = types.CodeWikiRunStatusPublished
	require.NoError(t, s.repo.UpdateRun(ctx, baseline))
	pages := []codeWikiPlannedPage{
		{Slug: "0", Title: "Retired", RepositoryID: "front", SourcePaths: []string{"removed.tsx"}, DependencyVersion: 1},
		{Slug: types.CodeWikiSummaryTopicKey, Title: "Guide"},
	}
	raw := []*types.WikiPage{
		{ID: "retired", KnowledgeBaseID: "kb", Slug: "0", Title: "Retired", Content: "Exact old bytes\r\n", Status: types.WikiPageStatusPublished, Version: 3},
		{ID: "guide", KnowledgeBaseID: "kb", Slug: types.CodeWikiSummaryTopicKey, Title: "Guide", Content: "[[0|Retired]]", Status: types.WikiPageStatusPublished, Version: 3},
	}
	guard := &types.CodeWikiPublicationGuard{TopicUpdates: true, BaselineRunID: baseline.ID, Pages: map[string]string{}}
	for i, page := range pages {
		require.NoError(t, db.Create(raw[i]).Error)
		guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(raw[i])
		blob, err := json.Marshal(page)
		require.NoError(t, err)
		require.NoError(t, s.repo.UpsertRunPage(ctx, &types.CodeWikiRunPage{RunID: baseline.ID, KnowledgeBaseID: "kb", PageKey: page.Slug, PlanMetadata: types.JSON(blob)}))
		pages[i].BaselineVersion = 3
	}
	run := &types.CodeWikiRun{TenantID: 7, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, s.repo.CreateRun(ctx, run))
	state := &codeWikiRunState{run: run, pages: pages, cfg: codeWikiRunConfig{MaxModelCalls: 10, TimeoutMinutes: 10, TopicConcurrency: 2,
		PublicationGuard: guard, SourceChanges: []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "removed.tsx", Kind: "deleted", BeforeHash: "old"}}}}
	require.NoError(t, s.prepareCodeWikiTopicImpact(ctx, state, pages, raw))
	require.Equal(t, "source_deleted", guard.Impact["0"].Kind)
	require.Equal(t, "archive", guard.Sections["0"][0].ID)
	guard.ReviewApproved = true
	require.NoError(t, s.persistPlannedPages(ctx, run, pages))
	require.NoError(t, s.startCodeWikiTopics(ctx, state, types.CodeWikiGeneratePayload{}))
	staged, err := s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	for _, page := range staged {
		require.Equal(t, types.CodeWikiRunPageStatusStaged, page.Status)
		if page.PageKey == "0" {
			require.Equal(t, raw[0].Content, page.Content)
		} else {
			require.NotContains(t, page.Content, "[[0")
		}
	}
	for _, before := range raw {
		var after types.WikiPage
		require.NoError(t, db.First(&after, "id = ?", before.ID).Error)
		require.Equal(t, types.CodeWikiPageFingerprint(before), types.CodeWikiPageFingerprint(&after))
	}
	rows, err := s.repo.(interfaces.CodeWikiTopicRepository).ListTopics(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	for _, topic := range rows {
		require.Equal(t, "completed", topic.Status)
		var cp types.CodeWikiTopicCheckpoint
		require.NoError(t, json.Unmarshal(topic.Checkpoint, &cp))
		require.Empty(t, cp.Items, "retired conclusions cannot appear in the new overview")
	}
	require.Zero(t, run.ModelCalls)
}

func TestCodeWikiArchiveLinkCleanupPreservesOtherBytes(t *testing.T) {
	before := "# Unchanged\r\n\n[[retired|Old entry]] / [[ KEEP |Keep]] / [[retired]]\r\n"
	after := removeArchivedCodeWikiLinks(before, map[string]string{"retired": "fingerprint"})
	require.Equal(t, "# Unchanged\r\n\nOld entry / [[ KEEP |Keep]] / retired\r\n", after)
}

func TestBuildPublishedWikiArchivePreservesContentAndRevision(t *testing.T) {
	live := &types.WikiPage{ID: "id", Slug: "retired", CategoryPath: types.StringArray{"项目概览"}, Content: "original\r\n", Summary: "old summary", Version: 9, Status: types.WikiPageStatusPublished}
	blob, err := json.Marshal(codeWikiRunConfig{PublicationGuard: &types.CodeWikiPublicationGuard{ArchivePages: map[string]string{"retired": types.CodeWikiPageFingerprint(live)}}})
	require.NoError(t, err)
	next, revision, err := buildPublishedWikiPage(&types.KnowledgeBase{}, &types.CodeWikiRun{ConfigSnapshot: types.JSON(blob)},
		&types.CodeWikiRunPage{PageKey: "retired", Category: types.CodeWikiPageCategoryOverview, Content: live.Content, Summary: live.Summary}, live, nil)
	require.NoError(t, err)
	require.Equal(t, types.WikiPageStatusArchived, next.Status)
	require.Equal(t, revision.Content, next.Content)
	require.Equal(t, revision.Summary, next.Summary)
	require.Equal(t, types.WikiPageStatusPublished, revision.Status)
	require.Equal(t, 9, revision.Version)
}

func TestCodeWikiIncrementalDoesNotResurrectArchivedTopics(t *testing.T) {
	s, db, baseline, _ := topicServiceFixture(t)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_retired_page_identity ON code_wiki_run_pages(run_id, page_key)`).Error)
	ctx := context.Background()
	for _, key := range []string{"active", "retired", types.CodeWikiSummaryTopicKey} {
		page := codeWikiPlannedPage{Slug: key, Title: key, RepositoryID: "front", SourcePaths: []string{key + ".ts"}}
		blob, err := json.Marshal(page)
		require.NoError(t, err)
		require.NoError(t, s.repo.UpsertRunPage(ctx, &types.CodeWikiRunPage{RunID: baseline.ID, KnowledgeBaseID: "kb", PageKey: key,
			Status: types.CodeWikiRunPageStatusPublished, PlanMetadata: types.JSON(blob)}))
		status := types.WikiPageStatusPublished
		if key == "retired" {
			status = types.WikiPageStatusArchived
		}
		require.NoError(t, db.Create(&types.WikiPage{ID: key, KnowledgeBaseID: "kb", Slug: key, Title: key, Status: status}).Error)
	}
	for round := 0; round < 4; round++ {
		state := &codeWikiRunState{run: &types.CodeWikiRun{KnowledgeBaseID: "kb"}, cfg: codeWikiRunConfig{DirectoryVersion: types.CodeWikiDirectoryVersionFrontend,
			PublicationGuard: &types.CodeWikiPublicationGuard{TopicUpdates: true, BaselineRunID: baseline.ID}}}
		pages, err := s.planOutline(ctx, state, nil, "Chinese")
		require.NoError(t, err)
		require.Len(t, pages, 2)
		for _, page := range pages {
			require.NotEqual(t, "retired", page.Slug)
		}
		require.Zero(t, state.run.ModelCalls)
	}
}
