package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiPublishRejectsIncompletePlan(t *testing.T) {
	run := &types.CodeWikiRun{TotalPages: 2}
	first := &types.CodeWikiRunPage{PageKey: "first", Status: types.CodeWikiRunPageStatusStaged, ValidationStatus: "passed"}
	second := &types.CodeWikiRunPage{PageKey: "second", Status: types.CodeWikiRunPageStatusPending, ValidationStatus: "generating"}
	require.Error(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first}))
	require.Error(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first, second}))
	second.Status, second.ValidationStatus = types.CodeWikiRunPageStatusStaged, "failed"
	require.Error(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first, second}))
	second.ValidationStatus, second.GenerationError = "passed", "unusable evidence"
	require.Error(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first, second}))
	second.GenerationError = ""
	require.NoError(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first, second}))
	run.FailedPages = 1
	require.Error(t, validateCodeWikiPublishCompleteness(run, []*types.CodeWikiRunPage{first, second}))
}

func TestCodeWikiDirectoryVersionDoesNotMigrateExistingPages(t *testing.T) {
	legacy := &types.CodeWikiRun{ConfigSnapshot: types.JSON(`{}`)}
	v1 := &types.CodeWikiRun{ConfigSnapshot: types.JSON(`{"directory_version":1}`)}
	oldFolders, err := codeWikiFoldersForRun(legacy)
	require.NoError(t, err)
	newFolders, err := codeWikiFoldersForRun(v1)
	require.NoError(t, err)
	require.Equal(t, []string{"系统架构", "前后端交互"}, oldFolders[types.CodeWikiPageCategoryInteraction])
	require.Equal(t, []string{"接口与跨端契约"}, newFolders[types.CodeWikiPageCategoryInteraction])
	require.Len(t, newFolders, 7)
	kb := &types.KnowledgeBase{ID: "kb-1"}
	candidate := &types.CodeWikiRunPage{PageKey: "contract", Category: types.CodeWikiPageCategoryInteraction}
	live := &types.WikiPage{ID: "page-1", Content: "original\n\nbytes", CategoryPath: types.StringArray{"系统架构", "前后端交互"}}
	_, _, err = buildPublishedWikiPage(kb, v1, candidate, live, nil)
	require.ErrorContains(t, err, "explicit migration")
	require.Equal(t, "original\n\nbytes", live.Content)
	require.Equal(t, types.StringArray{"系统架构", "前后端交互"}, live.CategoryPath)
	_, err = codeWikiFoldersForRun(&types.CodeWikiRun{ConfigSnapshot: types.JSON(`{"directory_version":99}`)})
	require.Error(t, err)
}
