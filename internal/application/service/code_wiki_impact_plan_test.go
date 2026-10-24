package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type codeWikiImpactEvidenceRepo struct{ interfaces.CodeWikiRepository }

func (codeWikiImpactEvidenceRepo) ListEvidenceByPageVersion(_ context.Context, _, id string, _ int) ([]*types.CodeWikiPageEvidence, error) {
	if id != "direct" {
		return nil, nil
	}
	return []*types.CodeWikiPageEvidence{{EvidenceKey: "E1", RepositoryID: "front", Path: "api.ts"}}, nil
}

func TestCodeWikiImpactProjectionDoesNotConfuseUnmatchedWithUnaffected(t *testing.T) {
	content := "## 请求\nrequest [E1]\n## 保持\nkeep\n"
	sections, err := types.CodeWikiSections(content)
	require.NoError(t, err)
	guard := &types.CodeWikiPublicationGuard{ReviewRequired: true, Sections: map[string][]types.CodeWikiSection{"direct": sections, "indirect": sections}}
	cfg := codeWikiRunConfig{PublicationGuard: guard, SourceChanges: []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "api.ts", Kind: "modified"}, {RepositoryID: "back", Path: "new.go", Kind: "added"}}}
	s := &codeWikiService{repo: codeWikiImpactEvidenceRepo{}}
	require.NoError(t, s.buildCodeWikiImpactPlan(context.Background(), []*types.WikiPage{{ID: "direct", Slug: "direct", Content: content}, {ID: "indirect", Slug: "indirect", Content: content}}, &cfg))
	require.Equal(t, "citation_changed", guard.Impact["direct"].Kind)
	require.Equal(t, []string{sections[1].ID}, guard.Impact["direct"].SectionIDs)
	require.Equal(t, "joint_review", guard.Impact["indirect"].Kind)
	require.Empty(t, guard.AllowedSections, "projection must never grant write permission")
}

func TestCodeWikiAdditiveDiscoveryCannotRedrawExistingDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.ts"), []byte("export function checkout() {}"), 0600))
	guard := &types.CodeWikiPublicationGuard{}
	state := &codeWikiRunState{cfg: codeWikiRunConfig{MaxPages: 5, SourceChanges: []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "new.ts", AfterHash: "hash"}}}, scopes: map[string]codebase.SnapshotScope{"front": {LocalPath: root}}}
	existing := []codeWikiPlannedPage{{Slug: "overview-a", Title: "固定标题", SortOrder: 13}}
	proposal := codeWikiPlannedPage{Title: "结算链路", InstanceKind: "workflow", RepositoryID: "front", Domain: "交易", SourcePaths: []string{"new.ts"}}
	pages, err := normalizeCodeWikiAdditions(state, existing, []codeWikiPlannedPage{proposal}, nil, guard)
	require.NoError(t, err)
	require.Len(t, pages, 2)
	require.Equal(t, existing[0], pages[0])
	require.NotEmpty(t, guard.NewPages[pages[1].Slug])
	require.Equal(t, "create", guard.Sections[pages[1].Slug][0].ID)
	require.Empty(t, guard.AllowedSections, "new pages require an explicit review selection")
	again, err := normalizeCodeWikiAdditions(state, pages, []codeWikiPlannedPage{proposal}, nil, guard)
	require.NoError(t, err)
	require.Equal(t, pages, again, "repeated proposals must not duplicate identities")
	proposal.SourcePaths = []string{"not-provided.ts"}
	_, err = normalizeCodeWikiAdditions(state, pages, []codeWikiPlannedPage{proposal}, nil, guard)
	require.Error(t, err)
}
