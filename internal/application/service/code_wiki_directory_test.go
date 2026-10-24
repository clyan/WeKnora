package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type codeWikiOutlineModel interface{ chat.Chat }

type codeWikiOutlineChat struct {
	codeWikiOutlineModel
	responses []string
	prompts   []string
}

func (m *codeWikiOutlineChat) Chat(_ context.Context, messages []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.prompts = append(m.prompts, messages[len(messages)-1].Content)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &types.ChatResponse{Content: response}, nil
}

func TestCodeWikiOutlineCorrectsDirectorySourceAnchors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "src"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte("export const api = 1\n"), 0600))
	invalid := `{"pages":[{"title":"接口","instance_kind":"contract","repository_id":"front","domain":"接口","source_paths":["src"]}]}`
	valid := `{"pages":[{"title":"接口","instance_kind":"contract","repository_id":"front","domain":"接口","source_paths":["src/api.ts"]}]}`
	for _, tc := range []struct {
		name, correction string
		wantError        string
	}{
		{"corrected file", valid, ""},
		{"still a directory", invalid, `source anchor "src"`},
		{"invalid JSON", `not JSON`, "expected non-empty pages JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &codeWikiOutlineChat{responses: []string{invalid, tc.correction}}
			state := &codeWikiRunState{run: &types.CodeWikiRun{}, chat: model,
				cfg:    codeWikiRunConfig{DirectoryVersion: 1, MaxPages: 10, MaxModelCalls: 2},
				scopes: map[string]codebase.SnapshotScope{"front": {LocalPath: root}}}
			pages, err := (&codeWikiService{}).planOutline(context.Background(), state,
				map[string]*codeWikiRepoSummary{"front": {Repo: "前端", Role: "frontend"}}, "Chinese")
			require.Len(t, model.prompts, 2)
			require.Contains(t, model.prompts[0], `"src/api.ts"`)
			require.Contains(t, model.prompts[1], `source anchor "src"`)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, pages)
				return
			}
			require.NoError(t, err)
			require.Len(t, pages, 1)
			require.Equal(t, []string{"src/api.ts"}, pages[0].SourcePaths)
			require.Equal(t, types.CodeWikiPageCategoryInteraction, pages[0].Category)
		})
	}
}

func TestCodeWikiPlanningFilesAreBoundedAndExcludeSymlinks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "src"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte("source"), 0600))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
	paths, err := codeWikiPlanningFiles(context.Background(), root, 100)
	require.NoError(t, err)
	require.Equal(t, []string{"src/api.ts"}, paths)
	paths, err = codeWikiPlanningFiles(context.Background(), root, 1)
	require.NoError(t, err)
	require.Empty(t, paths)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = codeWikiPlanningFiles(ctx, root, 100)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCodeWikiFixedDirectoryCatalogAndFallback(t *testing.T) {
	summaries := map[string]*codeWikiRepoSummary{
		"front-a": {Repo: "客户端A", Role: "frontend"},
		"back-b":  {Repo: "服务B", Role: "backend"},
		"back-c":  {Repo: "服务C", Role: "backend"},
	}
	state := &codeWikiRunState{cfg: codeWikiRunConfig{DirectoryVersion: 1, MaxPages: 60}}
	catalog := codeWikiTopicCatalog(summaries)
	require.Equal(t, catalog, codeWikiTopicCatalog(summaries))
	for _, topic := range catalog {
		require.NotContains(t, topic.CategoryPath, "依赖库")
		if topic.Category == types.CodeWikiPageCategoryRepository {
			require.Len(t, topic.CategoryPath, 2)
			require.Contains(t, topic.Slug, strings.ReplaceAll(topic.RepositoryID, "-", ""))
		}
	}
	for _, page := range codeWikiFallbackOutline(state, summaries) {
		normalized, err := normalizeCodeWikiV1Page(page, catalog, state, summaries)
		require.NoError(t, err)
		require.Equal(t, page.Title, normalized.Title)
		require.Equal(t, page.CategoryPath, normalized.CategoryPath)
	}
	page, err := normalizeCodeWikiV1Page(codeWikiPlannedPage{Slug: "overview-a", Title: "模型随意改名", Category: "invented"}, catalog, state, summaries)
	require.NoError(t, err)
	require.Equal(t, "项目介绍与能力地图", page.Title)
	require.Equal(t, types.CodeWikiPageCategoryOverview, page.Category)
	_, err = normalizeCodeWikiV1Page(codeWikiPlannedPage{Slug: "invented", Title: "没有源码依据的功能"}, catalog, state, summaries)
	require.Error(t, err)
	state.scopes = map[string]codebase.SnapshotScope{"front-a": {LocalPath: t.TempDir()}}
	_, err = normalizeCodeWikiV1Page(codeWikiPlannedPage{RepositoryID: "front-a", InstanceKind: "workflow", Title: "功能", Domain: "领域", SourcePaths: []string{"../outside.ts"}}, catalog, state, summaries)
	require.Error(t, err)
	_, err = codeWikiCandidatePath(&types.CodeWikiRun{ConfigSnapshot: types.JSON(`{"directory_version":1}`)}, &types.CodeWikiRunPage{
		Category: types.CodeWikiPageCategoryRepository, CategoryPath: types.StringArray{"任意目录"}})
	require.Error(t, err)
}
