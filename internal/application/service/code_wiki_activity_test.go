package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiExecutionActivityTracksRealReadsAndModelCalls(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("private source contents\n"), 0600))
	var events []types.CodeWikiRunEvent
	state := &codeWikiRunState{
		run:       &types.CodeWikiRun{Phase: "规划文档目录"},
		cfg:       codeWikiRunConfig{MaxModelCalls: 1},
		chat:      &codeWikiPatchChat{response: "outline"},
		repoNames: map[string]string{"front": "前端仓库"},
		activity: func(_ context.Context, kind, message string) {
			events = append(events, types.CodeWikiRunEvent{Kind: kind, Message: message})
		},
	}
	s := &codeWikiService{}
	scope := codebase.SnapshotScope{RepositoryID: "front", LocalPath: root}
	content, err := s.readSource(ctx, state, scope, "api.ts", 1, 1)
	require.NoError(t, err)
	require.Contains(t, content, "private source contents")
	require.Len(t, events, 2)
	require.Equal(t, "operation_started", events[0].Kind)
	require.Equal(t, "operation_completed", events[1].Kind)
	require.Contains(t, events[0].Message, "前端仓库 · api.ts:1–1")
	_, err = s.readSource(ctx, state, scope, "missing.ts", 1, 1)
	require.Error(t, err)
	require.Equal(t, "operation_failed", events[3].Kind)
	_, err = s.callModel(ctx, state, "private prompt contents")
	require.NoError(t, err)
	require.Equal(t, "operation_started", events[4].Kind)
	require.Equal(t, "operation_completed", events[5].Kind)
	require.Contains(t, events[4].Message, "调用生成模型 · 规划文档目录")
	_, err = s.callModel(ctx, state, "exhausted")
	require.ErrorIs(t, err, errCodeWikiBudgetPaused)
	require.Len(t, events, 6, "a request never started must not appear as running")
	for _, event := range events {
		require.NotContains(t, event.Message, "private source contents")
		require.NotContains(t, event.Message, "private prompt contents")
	}
}
