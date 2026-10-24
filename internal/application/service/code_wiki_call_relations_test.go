package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type codeWikiCallProvider struct {
	codebase.CodeAnalysisProvider
	queried []string
	err     error
}

func (p *codeWikiCallProvider) QueryRelations(_ context.Context, scope codebase.SnapshotScope, _ string, opts codebase.QueryOptions) ([]codebase.RelationEdge, error) {
	p.queried = append(p.queried, scope.RepositoryID)
	return []codebase.RelationEdge{{Kind: "call", FromRepositoryID: scope.RepositoryID, ToRepositoryID: scope.RepositoryID}}, p.err
}

func (*codeWikiCallProvider) SearchCode(context.Context, codebase.SnapshotScope, string, codebase.QueryOptions) ([]codebase.SearchHit, error) {
	return nil, nil
}

func TestCodeWikiCallPairedEvidence(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "caller.ts"), []byte("callee();\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "callee.ts"), []byte("function callee() {}\n"), 0o600))
	state := &codeWikiRunState{provider: &codeWikiCallProvider{}, scopes: map[string]codebase.SnapshotScope{
		"app": {RepositoryID: "app", SnapshotID: "snapshot", CommitSHA: "frozen-commit", LocalPath: root},
	}, relations: []codebase.RelationEdge{{Kind: "call", FromRepositoryID: "app", ToRepositoryID: "app",
		FromPath: "caller.ts", ToPath: "callee.ts", StartLine: 1, EndLine: 1, ToStartLine: 1, ToEndLine: 1}}}
	s := &codeWikiService{}
	page := codeWikiPlannedPage{Category: types.CodeWikiPageCategoryWorkflow}
	evidence := s.retrieveEvidence(context.Background(), state, page)
	require.Len(t, evidence, 2)
	require.Equal(t, "caller.ts", evidence[0].Path)
	require.Equal(t, "callee.ts", evidence[1].Path)
	for _, spec := range evidence {
		require.Equal(t, "frozen-commit", spec.CommitSHA)
	}
	state.relations[0].ToPath = "missing.ts"
	require.Empty(t, s.retrieveEvidence(context.Background(), state, page))
}

func TestCodeWikiCollectCallRelations(t *testing.T) {
	p := &codeWikiCallProvider{}
	state := &codeWikiRunState{provider: p, scopes: map[string]codebase.SnapshotScope{
		"frontend": {RepositoryID: "frontend"}, "backend": {RepositoryID: "backend"},
	}, relations: []codebase.RelationEdge{{Kind: "package_import"}}}
	require.NoError(t, collectCodeWikiCallRelations(context.Background(), state))
	require.Equal(t, []string{"backend", "frontend"}, p.queried)
	require.Len(t, state.relations, 3)
	require.Equal(t, "package_import", state.relations[0].Kind)
	state.capabilities = &types.CodeWikiCapabilityReport{Capabilities: []types.CodeWikiCapabilityStatus{{Capability: types.CodeWikiCapCallGraph, Available: true}}}
	require.Contains(t, codeWikiKnownLimits(state), "空结果不代表")
	p.err = errors.New("engine unavailable")
	require.ErrorContains(t, collectCodeWikiCallRelations(context.Background(), state), "engine unavailable")
	state.interruptCheck = func() error { return context.Canceled }
	require.ErrorIs(t, collectCodeWikiCallRelations(context.Background(), state), context.Canceled)
}
