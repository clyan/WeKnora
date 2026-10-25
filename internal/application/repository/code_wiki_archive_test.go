package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiArchivePublicationIsReviewedAtomicAndRetainsHistory(t *testing.T) {
	for _, scenario := range []string{"approved", "unapproved", "dangling", "human", "candidate_changed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db := newCodeWikiTestDB(t)
			repo := &codeWikiRepository{db: db}
			live := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: "retired", Title: "Retired", PageType: types.WikiPageTypeSynthesis,
				Content: "Exact old bytes\r\n", Summary: "old summary", Status: types.WikiPageStatusPublished, Version: 4, LastEditSource: types.WikiEditSourcePipeline}
			if scenario == "human" {
				live.LastEditSource = types.WikiEditSourceUser
			}
			require.NoError(t, db.Create(live).Error)
			other := &types.WikiPage{ID: uuid.NewString(), KnowledgeBaseID: "kb", Slug: "other", Title: "Other", Content: "Untouched\r\n", Status: types.WikiPageStatusPublished, Version: 2}
			if scenario == "dangling" {
				// Check the real markdown even when the cached OutLinks is empty.
				other.Content += "[[retired|Still linked]]"
			}
			require.NoError(t, db.Create(other).Error)
			originalHash, otherHash := types.CodeWikiPageFingerprint(live), types.CodeWikiPageFingerprint(other)
			guard := &types.CodeWikiPublicationGuard{TopicUpdates: true, ReviewApproved: true,
				Pages: map[string]string{"retired": originalHash, "other": otherHash}, AllowedPages: []string{"retired"},
				ArchivePages: map[string]string{"retired": originalHash}, AllowedSections: map[string]map[string]string{"retired": {"archive": originalHash}}}
			if scenario == "unapproved" {
				guard.AllowedSections["retired"] = map[string]string{"replace": originalHash}
			}
			blob, err := json.Marshal(map[string]any{"publication_guard": guard})
			require.NoError(t, err)
			run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusStaged, ConfigSnapshot: types.JSON(blob)}
			require.NoError(t, repo.CreateRun(ctx, run))
			candidate := &types.CodeWikiRunPage{KnowledgeBaseID: "kb", RunID: run.ID, PageKey: live.Slug, Title: live.Title, PageType: live.PageType,
				Content: live.Content, Summary: live.Summary, BaselineVersion: live.Version, Status: types.CodeWikiRunPageStatusStaged}
			if scenario == "candidate_changed" {
				candidate.Content = "rewritten"
			}
			require.NoError(t, repo.UpsertRunPage(ctx, candidate))
			oldEvidence := &types.CodeWikiPageEvidence{ID: uuid.NewString(), KnowledgeBaseID: "kb", PageID: live.ID, PageVersion: 4, EvidenceKey: "old"}
			require.NoError(t, db.Create(oldEvidence).Error)
			apply := func(tx interfaces.TX, candidate *types.CodeWikiRunPage, current *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
				next, revision, err := applyPageSimple(tx, candidate, current)
				next.Status, next.OutLinks = types.WikiPageStatusArchived, nil
				return next, revision, err
			}
			_, err = repo.PublishRun(ctx, run, []*types.CodeWikiRunPage{candidate}, evidenceFor(candidate), apply)
			var after, afterOther types.WikiPage
			require.NoError(t, db.First(&after, "id = ?", live.ID).Error)
			require.NoError(t, db.First(&afterOther, "id = ?", other.ID).Error)
			require.Equal(t, otherHash, types.CodeWikiPageFingerprint(&afterOther))
			if scenario != "approved" {
				require.Error(t, err)
				require.Equal(t, originalHash, types.CodeWikiPageFingerprint(&after), "failed publication must roll back every live write")
				var count int64
				require.NoError(t, db.Model(&types.WikiPageRevision{}).Count(&count).Error)
				require.Zero(t, count)
				return
			}
			require.NoError(t, err)
			require.Equal(t, types.WikiPageStatusArchived, after.Status)
			require.Equal(t, live.Content, after.Content)
			require.Equal(t, 5, after.Version)
			var revision types.WikiPageRevision
			require.NoError(t, db.First(&revision, "page_id = ?", live.ID).Error)
			require.Equal(t, 4, revision.Version)
			require.Equal(t, types.WikiPageStatusPublished, revision.Status)
			var count int64
			require.NoError(t, db.Model(&types.CodeWikiPageEvidence{}).Where("page_id = ?", live.ID).Count(&count).Error)
			require.EqualValues(t, 1, count, "keep historical evidence; never assign deleted source to the new archived version")
			visible, _, err := (&wikiPageRepository{db: db}).ListPagesCursor(ctx, "kb", "", 100)
			require.NoError(t, err)
			require.Len(t, visible, 1)
			require.Equal(t, "other", visible[0].Slug)
		})
	}
}
