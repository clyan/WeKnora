package codebase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func testBuiltinCLI(t *testing.T) *BuiltinProvider {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codebase-memory-mcp")
	// Exercise the actual process boundary and output formats, including the
	// flags that previously caused every evidence search to fail.
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codebase-memory-mcp ${TEST_ENGINE_VERSION:-0.12.1}"
  exit 0
fi
case "$2" in
  index_repository)
    case "$3" in *'"*"'*) exit 9;; esac
    echo '{"status":"success"}' ;;
  query_graph)
    echo '{"columns":["kind","from_symbol","to_symbol"],"rows":[]}' ;;
  search_code)
    case "$*" in *--query*) exit 8;; esac
    echo '{"cols":["qn","file","lines"],"rows":[["consumer.src.screen","src/screen.tsx","1-3"]],"raw_matches":{"cols":["file","line","content"],"rows":[]}}' ;;
  *) exit 7 ;;
esac
`), 0o700))
	return &BuiltinProvider{exec: &builtinExecutor{bin: bin}}
}

func testSnapshotFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
}

func TestBuiltinCapabilitiesVersion(t *testing.T) {
	p := testBuiltinCLI(t)
	for _, version := range []string{"0.12.1", "0.11.0", "0.12.10"} {
		t.Run(version, func(t *testing.T) {
			t.Setenv("TEST_ENGINE_VERSION", version)
			report, err := p.CheckCapabilities(context.Background(), nil)
			require.NoError(t, err)
			require.True(t, report.Connected)
			require.Equal(t, version == BuiltinSupportedVersion, report.HasCapability(types.CodeWikiCapCrossRepo))
			if version == BuiltinSupportedVersion {
				require.Empty(t, report.Notes)
			} else {
				require.Len(t, report.Notes, 1)
			}
		})
	}
}

func TestBuiltinCrossRepositoryBusinessPackages(t *testing.T) {
	p := testBuiltinCLI(t)
	consumer := SnapshotScope{KnowledgeBaseID: "kb", SnapshotID: "app", RepositoryID: "app", LocalPath: t.TempDir()}
	consumer.ArtifactHandle = consumer.ProjectID()
	library := SnapshotScope{KnowledgeBaseID: "kb", SnapshotID: "lib", RepositoryID: "lib", LocalPath: t.TempDir()}
	library.ArtifactHandle = library.ProjectID()
	testSnapshotFile(t, consumer.LocalPath, "package.json", `{
  "name": "business-app",
  "dependencies": {"@business/orders":"^1.0.0", "react":"^19"},
  "devDependencies": {"@business/lint":"^1.0.0"}
}`)
	testSnapshotFile(t, consumer.LocalPath, "src/screen.tsx", "import { Orders } from '@business/orders';\nexport const Screen = Orders;\n")
	testSnapshotFile(t, library.LocalPath, "packages/orders/package.json", "{\n  \"name\": \"@business/orders\",\n  \"version\": \"2.0.0\"\n}\n")
	testSnapshotFile(t, library.LocalPath, "packages/lint/package.json", `{"name":"@business/lint"}`)
	edges, err := p.QueryCrossRepository(context.Background(), []SnapshotScope{library, consumer}, "", QueryOptions{Limit: 20})
	require.NoError(t, err)
	require.Len(t, edges, 2)
	require.Equal(t, "package_dependency", edges[0].Kind)
	require.Equal(t, "package_import", edges[1].Kind)
	for _, edge := range edges {
		require.Equal(t, "app", edge.FromRepositoryID)
		require.Equal(t, "lib", edge.ToRepositoryID)
		require.Equal(t, "@business/orders", edge.ToSymbol)
		require.Equal(t, "packages/orders/package.json", edge.ToPath)
		require.Equal(t, 2, edge.ToStartLine)
	}
	require.Equal(t, 3, edges[0].StartLine)
	require.Equal(t, "src/screen.tsx", edges[1].FromPath)
	require.Equal(t, 1, edges[1].StartLine)
	library.KnowledgeBaseID = "other-tenant-kb"
	_, err = p.QueryCrossRepository(context.Background(), []SnapshotScope{consumer, library}, "", QueryOptions{})
	require.ErrorContains(t, err, "one knowledge base")
}

// Opt-in integration check against the user's existing frozen indexes. This
// does not call a model, publish pages, download repositories, or reindex code.
func TestBuiltinInstalledSnapshots(t *testing.T) {
	root := os.Getenv("WEKNORA_TEST_CODE_WIKI_SNAPSHOTS")
	if root == "" {
		t.Skip("set WEKNORA_TEST_CODE_WIKI_SNAPSHOTS for local CLI integration")
	}
	kb := filepath.Base(root)
	dirs, err := os.ReadDir(root)
	require.NoError(t, err)
	var scopes []SnapshotScope
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		id := dir.Name()
		scope := SnapshotScope{KnowledgeBaseID: kb, SnapshotID: id, RepositoryID: id, LocalPath: filepath.Join(root, id)}
		scope.ArtifactHandle = scope.ProjectID()
		scopes = append(scopes, scope)
	}
	require.GreaterOrEqual(t, len(scopes), 2)
	p := NewBuiltinProvider()
	report, err := p.CheckCapabilities(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, report.Notes)
	edges, err := p.QueryCrossRepository(context.Background(), scopes, "", QueryOptions{Limit: 24})
	require.NoError(t, err)
	require.NotEmpty(t, edges)
	for _, edge := range edges {
		require.False(t, strings.Contains(edge.ToSymbol, "eslint"))
		t.Logf("%s: %s:%d -> %s:%d (%s)", edge.Kind, edge.FromPath, edge.StartLine, edge.ToPath, edge.ToStartLine, edge.ToSymbol)
	}
}
