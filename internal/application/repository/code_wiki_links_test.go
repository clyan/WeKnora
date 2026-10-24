package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCodeWikiBacklinkProjectionNeverWritesProtectedPages(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.WikiPage{}))
	for _, page := range []*types.WikiPage{
		{ID: "source", KnowledgeBaseID: "kb", Slug: "source", Status: types.WikiPageStatusPublished, OutLinks: types.StringArray{"target"}},
		{ID: "target", KnowledgeBaseID: "kb", Slug: "target", Status: types.WikiPageStatusPublished, PageMetadata: types.JSON(`{"code_wiki_run_id":"run"}`), InLinks: types.StringArray{"stale"}},
		{ID: "foreign", KnowledgeBaseID: "other", Slug: "foreign", Status: types.WikiPageStatusPublished, OutLinks: types.StringArray{"target"}},
	} {
		require.NoError(t, db.Create(page).Error)
	}
	var before types.WikiPage
	require.NoError(t, db.First(&before, "id = ?", "target").Error)
	repo := NewWikiPageRepository(db)
	ctx := context.Background()
	for _, get := range []func() (*types.WikiPage, error){
		func() (*types.WikiPage, error) { return repo.GetBySlug(ctx, "kb", "target") },
		func() (*types.WikiPage, error) { return repo.GetByID(ctx, "target") },
	} {
		page, err := get()
		require.NoError(t, err)
		require.Equal(t, types.StringArray{"source"}, page.InLinks)
	}
	pages, err := repo.ListAll(ctx, "kb")
	require.NoError(t, err)
	for _, page := range pages {
		if page.Slug == "target" {
			require.Equal(t, types.StringArray{"source"}, page.InLinks)
		}
	}
	require.NoError(t, db.Model(&types.WikiPage{}).Where("id = ?", "source").UpdateColumn("out_links", types.StringArray{}).Error)
	page, err := repo.GetByID(ctx, "target")
	require.NoError(t, err)
	require.Empty(t, page.InLinks)
	var after types.WikiPage
	require.NoError(t, db.First(&after, "id = ?", "target").Error)
	require.Equal(t, types.CodeWikiPageFingerprint(&before), types.CodeWikiPageFingerprint(&after))
}
