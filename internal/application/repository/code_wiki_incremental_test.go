package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiTopicIncrementalPublicationPreservesUnchangedRowsAcrossRounds(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()
	for _, slug := range []string{"changed", "untouched"} {
		require.NoError(t, db.Create(&types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: slug,
			Title: slug, PageType: types.WikiPageTypeSynthesis, Content: "# " + slug + "\r\n\nOriginal bytes.\n", Version: 3,
			Status: types.WikiPageStatusPublished, SortOrder: 17, LastEditSource: types.WikiEditSourcePipeline}).Error)
	}
	require.NoError(t, db.Exec(`CREATE TABLE incremental_writes (slug TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER audit_incremental_updates AFTER UPDATE ON wiki_pages
 BEGIN INSERT INTO incremental_writes VALUES (NEW.slug); END`).Error)
	baseline := ""
	untouchedHash := ""
	for round := 0; round < 4; round++ {
		raw, err := repo.ListRawWikiPages(ctx, "kb")
		require.NoError(t, err)
		guard := &types.CodeWikiPublicationGuard{TopicUpdates: true, ReviewApproved: true, BaselineRunID: baseline,
			Pages: map[string]string{}, AllowedPages: []string{"changed", "untouched"}, AllowedSections: map[string]map[string]string{}}
		var changed *types.WikiPage
		for _, page := range raw {
			guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
			if page.Slug == "changed" {
				changed = page
			} else {
				if untouchedHash == "" {
					untouchedHash = types.CodeWikiPageFingerprint(page)
				}
				require.Equal(t, untouchedHash, types.CodeWikiPageFingerprint(page))
			}
		}
		guard.AllowedSections["changed"] = map[string]string{"replace": types.CodeWikiPageFingerprint(changed)}
		blob, err := json.Marshal(map[string]any{"publication_guard": guard})
		require.NoError(t, err)
		run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusStaged, TotalPages: 2, ConfigSnapshot: types.JSON(blob)}
		require.NoError(t, repo.CreateRun(ctx, run))
		content := "# changed\n\nFirst change.\n"
		if round >= 2 {
			content = "# changed\n\nSecond change.\n"
		}
		candidate := &types.CodeWikiRunPage{KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "changed", Title: changed.Title,
			PageType: changed.PageType, Content: content, Status: types.CodeWikiRunPageStatusStaged, BaselineVersion: changed.Version}
		preserved := &types.CodeWikiRunPage{KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "untouched", Status: types.CodeWikiRunPageStatusPreserved}
		require.NoError(t, repo.UpsertRunPage(ctx, candidate))
		require.NoError(t, repo.UpsertRunPage(ctx, preserved))
		_, err = repo.PublishRun(ctx, run, []*types.CodeWikiRunPage{preserved, candidate}, evidenceFor(candidate), applyPageSimple)
		require.NoError(t, err)
		baseline = run.ID
	}
	var untouched *types.WikiPage
	require.NoError(t, db.Where("slug = ?", "untouched").First(&untouched).Error)
	require.Equal(t, untouchedHash, types.CodeWikiPageFingerprint(untouched), "includes version, timestamps, directory and exact content")
	var writes int64
	require.NoError(t, db.Table("incremental_writes").Where("slug = ?", "untouched").Count(&writes).Error)
	require.Zero(t, writes, "preservation must perform zero UPDATEs")
	require.NoError(t, db.Table("incremental_writes").Where("slug = ?", "changed").Count(&writes).Error)
	require.EqualValues(t, 2, writes, "identical candidate rounds also cause zero live writes")
}

func TestCodeWikiTopicReplacementCannotWriteOutsideReviewedScope(t *testing.T) {
	f := setupPublishFixture(t, false)
	guard := &types.CodeWikiPublicationGuard{TopicUpdates: true, ReviewApproved: true, Pages: map[string]string{}, AllowedPages: []string{f.update.PageKey}}
	raw, err := f.repo.ListRawWikiPages(context.Background(), "kb-1")
	require.NoError(t, err)
	for _, page := range raw {
		guard.Pages[page.Slug] = types.CodeWikiPageFingerprint(page)
		if page.Slug == f.update.PageKey {
			f.update.Title = page.Title
		}
	}
	blob, err := json.Marshal(map[string]any{"publication_guard": guard})
	require.NoError(t, err)
	f.run.ConfigSnapshot = types.JSON(blob)
	require.NoError(t, f.repo.UpdateRun(context.Background(), f.run))
	_, err = f.repo.PublishRun(context.Background(), f.run, []*types.CodeWikiRunPage{f.update}, nil, applyPageSimple)
	require.ErrorContains(t, err, "outside the reviewed impact scope")
}
