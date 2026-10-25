package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func frontendCatalogFixture(t *testing.T, files map[string]string) (*codeWikiRunState, map[string]*codeWikiRepoSummary) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0600))
	}
	return &codeWikiRunState{run: &types.CodeWikiRun{}, cfg: codeWikiRunConfig{DirectoryVersion: types.CodeWikiDirectoryVersionFrontend, MaxPages: 1, MaxModelCalls: 30},
			scopes: map[string]codebase.SnapshotScope{"front": {RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", LocalPath: root}}},
		map[string]*codeWikiRepoSummary{"front": {Repo: "app/main", Role: types.CodeWikiRepoRoleFrontend}}
}

func frontendReply(t *testing.T, reply frontendPlannerReply) string {
	t.Helper()
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	return string(data)
}

func frontendTopic(category, key string, paths ...string) codeWikiPlannedPage {
	return codeWikiPlannedPage{RepositoryID: "front", Category: category, Domain: key, Title: key, Scope: "evidenced topic " + key, SourcePaths: paths}
}

func TestCodeWikiFrontendPlansNonstandardLayoutsAndSemanticGranularity(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"unusual/order.screen":     strings.Repeat("// preface\n", 500) + "submitOrder()\n",
		"transport/channel.custom": "export const submitOrder = request('/orders')\n",
	})
	pages := []codeWikiPlannedPage{
		frontendTopic("frontend_page", "order-entry", "unusual/order.screen", "transport/channel.custom"),
		frontendTopic("frontend_api", "create-order", "transport/channel.custom"),
		frontendTopic("frontend_workflow", "order-submit", "unusual/order.screen", "transport/channel.custom"),
	}
	model := &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "unusual/order.screen", StartLine: 501, EndLine: 501, Notes: "Custom layout; real submit handler is late in file."}),
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "transport/channel.custom", StartLine: 1, EndLine: 80, Notes: "The request is called by the order screen."}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Pages: pages, Coverage: []frontendCoverage{
			{"unusual/order.screen", "documented", "order entry"}, {"transport/channel.custom", "documented", "request and workflow"},
		}}),
	}}
	state.chat = model
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{Pages: map[string]string{}}
	checkpoints := 0
	state.checkpoint = func(context.Context) error { checkpoints++; return nil }
	s := &codeWikiService{}
	planned, err := s.planOutline(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, planned, 3, "page count is independent of the obsolete MaxPages setting")
	require.Equal(t, 3, checkpoints)
	require.Contains(t, model.prompts[1], "submitOrder()")
	for _, page := range planned {
		require.NotEqual(t, page.Title, page.SourcePaths[0], "titles describe topics, not paths")
		_, err := codeWikiCandidatePath(&types.CodeWikiRun{ConfigSnapshot: types.JSON(`{"directory_version":2}`)},
			&types.CodeWikiRunPage{Category: page.Category, CategoryPath: page.CategoryPath})
		require.NoError(t, err)
		if page.Category == "frontend_page" {
			evidence := s.frontendAnchorEvidence(context.Background(), state, page)
			require.Len(t, evidence, 2)
			require.Equal(t, 501, evidence[1].StartLine, "generation preserves the inspected late-file source range")
		}
	}
	prior := planned[0]
	summaries["front"].Repo = "renamed"
	repeated, err := normalizeFrontendPage(prior, state, summaries)
	require.NoError(t, err)
	require.Equal(t, prior.Slug, repeated.Slug, "display name and source list cannot change semantic identity")
}

func TestCodeWikiFrontendPlanningBatchesResumeWithoutSkippingInventory(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 65; i++ {
		files[fmt.Sprintf("custom/%03d.txt", i)] = "project material\n"
	}
	state, summaries := frontendCatalogFixture(t, files)
	inventory, err := frontendInventory(context.Background(), state.scopes["front"].LocalPath)
	require.NoError(t, err)
	var finishReplies []string
	for offset := 0; offset < len(inventory); {
		batch := frontendInventoryBatch(inventory, offset)
		reply := frontendPlannerReply{Action: "finish", Notes: "Remember custom project conventions."}
		for _, name := range batch {
			reply.Coverage = append(reply.Coverage, frontendCoverage{name, "irrelevant", "auxiliary fixture material"})
		}
		if offset == 0 {
			reply.Pages = []codeWikiPlannedPage{frontendTopic("frontend_reference", "project-material", inventory[0])}
			reply.Coverage[0].Status = "documented"
		}
		finishReplies = append(finishReplies, frontendReply(t, reply))
		offset += len(batch)
	}
	state.cfg.MaxModelCalls = 2
	state.chat = &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: inventory[0], StartLine: 1, EndLine: 80}),
		finishReplies[0],
	}}
	var saved []byte
	state.checkpoint = func(context.Context) error { var err error; saved, err = json.Marshal(state.cfg); return err }
	s := &codeWikiService{}
	pages, err := s.planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiRunPaused)
	require.Empty(t, pages, "an unfinished directory is not a publishable plan")
	require.Equal(t, 32, state.cfg.FrontendPlanning.Repositories["front"].Cursor)
	var restored codeWikiRunConfig
	require.NoError(t, json.Unmarshal(saved, &restored))
	state.cfg = restored
	state.modelCallsAtStart = state.run.ModelCalls
	state.chat = &codeWikiOutlineChat{responses: finishReplies[1:]}
	pages, err = s.planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Equal(t, types.StringArray{"references", "app／main"}, pages[0].CategoryPath)
	require.Equal(t, 6000, pages[0].SortOrder, "resume rebuilds trusted directory metadata")
	require.Equal(t, 65, state.cfg.FrontendPlanning.Repositories["front"].Cursor)
	require.Len(t, state.cfg.FrontendPlanning.Repositories["front"].Decisions, 65)
	require.Equal(t, 4, state.run.ModelCalls, "usage remains cumulative while each resumed pass has an allowance")
}

func TestCodeWikiFrontendHasNoTwoHundredDocumentCeiling(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"custom.bundle": "many independently evidenced declarations\n"})
	finish := frontendPlannerReply{Action: "finish", Coverage: []frontendCoverage{{"custom.bundle", "documented", "multiple topics"}}}
	for i := 0; i < 201; i++ {
		finish.Pages = append(finish.Pages, frontendTopic("frontend_api", fmt.Sprintf("endpoint-%d", i), "custom.bundle"))
	}
	state.chat = &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "custom.bundle", StartLine: 1, EndLine: 1}),
		frontendReply(t, frontendPlannerReply{Action: "propose", Pages: finish.Pages[:100]}),
		frontendReply(t, frontendPlannerReply{Action: "propose", Pages: finish.Pages[100:]}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Coverage: finish.Coverage}),
	}}
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 201)
}

func TestCodeWikiFrontendRejectsUnreadInventedAndUnaccountedSources(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"custom.ui": "real source\n", "other.ui": "real source\n"})
	for _, anchor := range []string{"../outside.ts", "missing.ts", "."} {
		_, err := normalizeFrontendPage(frontendTopic("frontend_page", "bad", anchor), state, summaries)
		require.Error(t, err)
	}
	progress := &frontendRepoPlan{}
	reply := frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{frontendTopic("frontend_page", "topic", "custom.ui")},
		Coverage: []frontendCoverage{{"custom.ui", "documented", ""}}}
	require.ErrorContains(t, finishFrontendBatch(state, "front", reply, summaries, []string{"custom.ui"}, progress), "was not read")
	progress.ReadSpans = []codeWikiSourceSpan{{"custom.ui", 1, 1}}
	require.ErrorContains(t, finishFrontendBatch(state, "front", reply, summaries, []string{"custom.ui", "other.ui"}, progress), "unaccounted")
	require.Zero(t, progress.Cursor)
	require.Empty(t, progress.Pages, "rejected batch cannot mutate accepted topics")
	forged := reply
	forged.Pages = append([]codeWikiPlannedPage(nil), reply.Pages...)
	forged.Pages[0].SourceSpans = []codeWikiSourceSpan{{"custom.ui", 2, 2}}
	require.ErrorContains(t, finishFrontendBatch(state, "front", forged, summaries, []string{"custom.ui"}, progress), "not actually read")
	reply.Coverage = append(reply.Coverage, frontendCoverage{"other.ui", "unsupported", "requires an unavailable parser"})
	require.NoError(t, finishFrontendBatch(state, "front", reply, summaries, []string{"custom.ui", "other.ui"}, progress))
	require.Contains(t, progress.Decisions["other.ui"], "unsupported:")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&codeWikiService{}).planFrontendCatalog(ctx, state, summaries, "Chinese")
	require.ErrorIs(t, err, context.Canceled)
}

func TestCodeWikiFrontendIncrementalDiscoveryKeepsReviewAndIgnoresOldPageCap(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"custom.ui": "old entry\n", "feature.new": "new request\n"})
	existing, err := normalizeFrontendPage(frontendTopic("frontend_page", "old-entry", "custom.ui"), state, summaries)
	require.NoError(t, err)
	state.cfg.SourceChanges = []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "feature.new", AfterHash: "hash"}}
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{}
	state.chat = &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "feature.new", StartLine: 1, EndLine: 1}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{frontendTopic("frontend_api", "new-request", "feature.new")},
			Coverage: []frontendCoverage{{"custom.ui", "documented", "existing entry"}, {"feature.new", "documented", "new request"}}}),
	}}
	updated, err := (&codeWikiService{}).discoverCodeWikiAdditions(context.Background(), state, []codeWikiPlannedPage{existing}, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, updated, 2, "obsolete MaxPages=1 must not truncate incremental discovery")
	require.Len(t, state.cfg.PublicationGuard.NewPages, 1)
	require.Len(t, state.cfg.PublicationGuard.Sections[updated[1].Slug], 1, "new knowledge still requires explicit review")
}

func TestCodeWikiFrontendInvalidModelDoesNotFallbackToGenericDirectory(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"custom.ui": "real source"})
	state.chat = &codeWikiOutlineChat{responses: []string{"invalid", "invalid", "invalid"}}
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "invalid actions")
	require.Empty(t, pages)
}
