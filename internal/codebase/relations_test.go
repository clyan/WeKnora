package codebase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinCallRelations(t *testing.T) {
	ctx := context.Background()
	scope := SnapshotScope{KnowledgeBaseID: "kb", SnapshotID: "snapshot", RepositoryID: "repo", LocalPath: t.TempDir()}
	scope.ArtifactHandle = scope.ProjectID()
	testSnapshotFile(t, scope.LocalPath, "caller.ts", "function caller() { callee(); }\n")
	testSnapshotFile(t, scope.LocalPath, "callee.ts", "function callee() {}\n")
	bin := filepath.Join(t.TempDir(), "engine")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CALL_ARGS", argsFile)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CALL_ARGS\"\nprintf '%s' \"$CALL_RESULT\"\n"), 0o700))
	p := &BuiltinProvider{exec: &builtinExecutor{bin: bin}}
	columns := []string{"kind", "from_symbol", "from_path", "from_line", "from_end", "to_symbol", "to_path", "to_line", "to_end"}
	row := []any{"CALLS", scope.ArtifactHandle + ".caller", "caller.ts", "1", "1", scope.ArtifactHandle + ".callee", "callee.ts", 1, 1}
	setResult := func(cols []string, rows [][]any) {
		blob, err := json.Marshal(map[string]any{"columns": cols, "rows": rows})
		require.NoError(t, err)
		t.Setenv("CALL_RESULT", string(blob))
	}
	setResult(columns, [][]any{row})
	edges, err := p.QueryRelations(ctx, scope, "callee\" OR true //", QueryOptions{Limit: 2, Offset: 3})
	require.NoError(t, err)
	require.Len(t, edges, 1)
	require.Equal(t, "call", edges[0].Kind)
	require.Equal(t, "repo", edges[0].ToRepositoryID)
	require.Equal(t, 1, edges[0].StartLine)
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Contains(t, string(args), "--project\n"+scope.ProjectID()+"\n")
	require.Contains(t, string(args), `a.name = "callee\" OR true //"`)
	require.Contains(t, string(args), "SKIP 3 LIMIT 2")
	for _, tc := range []struct {
		name  string
		index int
		value any
		want  string
	}{
		{"foreign project", 5, "other.project.callee", "unexpected"},
		{"traversal", 6, "../callee.ts", "traversal"},
		{"stale line", 7, 999, "line range"},
		{"wrong relation", 0, "IMPORTS", "unexpected"},
		{"bad integer", 3, "invalid", "invalid call graph"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]any(nil), row...)
			bad[tc.index] = tc.value
			setResult(columns, [][]any{bad})
			_, err := p.QueryRelations(ctx, scope, "", QueryOptions{})
			require.ErrorContains(t, err, tc.want)
		})
	}
	setResult([]string{"kind"}, nil)
	_, err = p.QueryRelations(ctx, scope, "", QueryOptions{})
	require.ErrorContains(t, err, "missing column")
	setResult(columns, nil)
	edges, err = p.QueryRelations(ctx, scope, "", QueryOptions{})
	require.NoError(t, err)
	require.Empty(t, edges)
	_, err = p.QueryRelations(ctx, scope, "", QueryOptions{Offset: -1})
	require.ErrorContains(t, err, "negative")
	// Existing source outside the frozen tree must not become evidence via a symlink.
	testSnapshotFile(t, filepath.Dir(bin), "outside.ts", "outside source\n")
	require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(bin), "outside.ts"), filepath.Join(scope.LocalPath, "escape.ts")))
	bad := append([]any(nil), row...)
	bad[6] = "escape.ts"
	setResult(columns, [][]any{bad})
	_, err = p.QueryRelations(ctx, scope, "", QueryOptions{})
	require.ErrorContains(t, err, "escapes snapshot")
	scope.ArtifactHandle = "another-tenant"
	_, err = p.QueryRelations(ctx, scope, "", QueryOptions{})
	require.ErrorContains(t, err, "owned frozen snapshot")
}

// Reads existing indexes only: no indexing, cross-repo linking, model or publish.
func TestBuiltinInstalledCallRelations(t *testing.T) {
	root := os.Getenv("WEKNORA_TEST_CODE_WIKI_SNAPSHOTS")
	if root == "" {
		t.Skip("set WEKNORA_TEST_CODE_WIKI_SNAPSHOTS for local CLI integration")
	}
	dirs, err := os.ReadDir(root)
	require.NoError(t, err)
	p := NewBuiltinProvider()
	count := 0
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		scope := SnapshotScope{KnowledgeBaseID: filepath.Base(root), SnapshotID: dir.Name(), RepositoryID: dir.Name(), LocalPath: filepath.Join(root, dir.Name())}
		scope.ArtifactHandle = scope.ProjectID()
		edges, err := p.QueryRelations(context.Background(), scope, "", QueryOptions{Limit: 24})
		require.NoError(t, err)
		count += len(edges)
		if len(edges) > 0 {
			t.Logf("%s: %d source-verified call candidates", scope.RepositoryID, len(edges))
			targeted, err := p.QueryRelations(context.Background(), scope, edges[0].ToSymbol, QueryOptions{Limit: 2})
			require.NoError(t, err)
			require.NotEmpty(t, targeted)
			for _, edge := range targeted {
				require.True(t, edge.FromSymbol == edges[0].ToSymbol || edge.ToSymbol == edges[0].ToSymbol)
			}
		}
	}
	require.Positive(t, count)
}
