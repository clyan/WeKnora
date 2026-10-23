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

func TestCodeWikiCrossRepositoryPairedEvidence(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"cols\":[\"file\"],\"rows\":[]}'\n"), 0o700))
	t.Setenv("WEKNORA_CODEBASE_CLI_PATH", bin)
	p := codebase.NewBuiltinProvider()
	fromDir, toDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fromDir, "package.json"), []byte(`{"dependencies":{"@business/orders":"1"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(toDir, "package.json"), []byte(`{"name":"@business/orders"}`), 0o600))
	state := &codeWikiRunState{
		provider: p, builtin: p,
		scopes: map[string]codebase.SnapshotScope{
			"app":    {RepositoryID: "app", SnapshotID: "app-snapshot", CommitSHA: "app-commit", LocalPath: fromDir},
			"orders": {RepositoryID: "orders", SnapshotID: "orders-snapshot", CommitSHA: "orders-commit", LocalPath: toDir},
		},
		relations: []codebase.RelationEdge{{FromRepositoryID: "app", ToRepositoryID: "orders",
			FromPath: "package.json", ToPath: "package.json", StartLine: 1, EndLine: 1, ToStartLine: 1, ToEndLine: 1}},
	}
	s := &codeWikiService{}
	evidence := s.retrieveEvidence(context.Background(), state, codeWikiPlannedPage{Category: types.CodeWikiPageCategoryArchitecture})
	require.Len(t, evidence, 2)
	require.Equal(t, "app-commit", evidence[0].CommitSHA)
	require.Equal(t, "orders-commit", evidence[1].CommitSHA)
	require.Contains(t, evidence[0].Quote, "dependencies")
	require.Contains(t, evidence[1].Quote, "name")
	// Dependency evidence cannot make an unrelated DevOps page publishable.
	require.Empty(t, s.retrieveEvidence(context.Background(), state, codeWikiPlannedPage{Category: types.CodeWikiPageCategoryDevOps}))
	// Missing target source rejects the whole pair, rather than asserting a
	// relationship from one side's manifest alone.
	state.relations[0].ToPath = "missing.json"
	require.Empty(t, s.retrieveEvidence(context.Background(), state, codeWikiPlannedPage{Category: types.CodeWikiPageCategoryArchitecture}))
}
