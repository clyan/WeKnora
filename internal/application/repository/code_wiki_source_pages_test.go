package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiSourcePagesUseCurrentVersionsAndPaginateDistinctDocuments(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := &codeWikiRepository{db: db}
	ctx := context.Background()
	for i := 0; i < 205; i++ {
		id := uuid.NewString()
		require.NoError(t, db.Create(&types.WikiPage{ID: id, KnowledgeBaseID: "kb", Slug: fmt.Sprintf("page-%03d", i), Title: "文档", Status: types.WikiPageStatusPublished, Version: 3}).Error)
		for j := 0; j < 2; j++ {
			snapshot := "target"
			if i == 0 {
				snapshot = "historical"
			}
			require.NoError(t, db.Create(&types.CodeWikiPageEvidence{ID: uuid.NewString(), KnowledgeBaseID: "kb", PageID: id, PageVersion: 3, RepositoryID: "front", Path: "api.ts", SnapshotID: snapshot, EvidenceKey: fmt.Sprintf("E%d", j+1)}).Error)
		}
	}
	for i, scope := range []string{"old-version", "foreign-repo", "foreign-kb", "archived"} {
		page := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: "excluded-" + scope, Version: 3, Status: types.WikiPageStatusPublished}
		evidence := &types.CodeWikiPageEvidence{ID: uuid.NewString(), KnowledgeBaseID: "kb", PageID: page.ID, PageVersion: 3, RepositoryID: "front", Path: "api.ts", SnapshotID: "target", EvidenceKey: "E1"}
		switch i {
		case 0:
			evidence.PageVersion = 2
		case 1:
			evidence.RepositoryID = "other"
		case 2:
			page.KnowledgeBaseID = "other"
			evidence.KnowledgeBaseID = "other"
		case 3:
			page.Status = types.WikiPageStatusArchived
		}
		require.NoError(t, db.Create(page).Error)
		require.NoError(t, db.Create(evidence).Error)
	}
	first, err := repo.ListSourcePages(ctx, "kb", "front", "target", "api.ts", "")
	require.NoError(t, err)
	require.Len(t, first.Pages, 200)
	require.Equal(t, "page-199", first.NextCursor)
	require.False(t, first.Pages[0].MatchesSnapshot)
	require.True(t, first.Pages[1].MatchesSnapshot)
	second, err := repo.ListSourcePages(ctx, "kb", "front", "target", "api.ts", first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Pages, 5)
	require.Empty(t, second.NextCursor)
}
