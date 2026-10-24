package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type codeWikiDiffRepository struct {
	interfaces.CodeWikiRepository
	snapshots map[string]*types.CodeWikiSnapshot
}

func (r codeWikiDiffRepository) GetSnapshot(_ context.Context, kbID, id string) (*types.CodeWikiSnapshot, error) {
	snapshot := r.snapshots[id]
	if snapshot == nil || snapshot.KnowledgeBaseID != kbID {
		return nil, types.ErrCodeWikiSnapshotNotFound
	}
	return snapshot, nil
}

func TestCodeWikiJointNetDiffAcrossBranchesAndSuccessiveRounds(t *testing.T) {
	r := &codeWikiDiffRepository{snapshots: map[string]*types.CodeWikiSnapshot{}}
	s := &codeWikiService{repo: r}
	makeSnapshot := func(id, repo string, files map[string]string) *types.CodeWikiRunRepository {
		root := t.TempDir()
		for name, content := range files {
			path := filepath.Join(root, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(content), 0600))
		}
		r.snapshots[id] = &types.CodeWikiSnapshot{ID: id, KnowledgeBaseID: "kb", RepositoryID: repo,
			CommitSHA: id, FileCount: len(files), LocalPath: root}
		return &types.CodeWikiRunRepository{RepositoryID: repo, SnapshotID: id, CommitSHA: id}
	}
	f0 := makeSnapshot("F0", "frontend", map[string]string{"route.ts": "original", "unchanged.ts": "keep \r\n"})
	f1 := makeSnapshot("unrelated-front-branch", "frontend", map[string]string{"route.ts": "changed", "unchanged.ts": "keep \r\n", "new.ts": "entry"})
	b0 := makeSnapshot("B0", "backend", map[string]string{"handler.go": "original", "removed.go": "delete"})
	b1 := makeSnapshot("unrelated-back-branch", "backend", map[string]string{"handler.go": "changed"})
	ctx := context.Background()
	first, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f0, b0}, []*types.CodeWikiRunRepository{b0, f1})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "new.ts", first[0].Path)
	require.Equal(t, "added", first[0].Kind)
	require.Equal(t, "route.ts", first[1].Path)
	second, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f1, b0}, []*types.CodeWikiRunRepository{f1, b1})
	require.NoError(t, err)
	require.Len(t, second, 2)
	for _, change := range second {
		require.Equal(t, "backend", change.RepositoryID)
	}
	joint, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f0, b0}, []*types.CodeWikiRunRepository{f1, b1})
	require.NoError(t, err)
	require.Len(t, joint, 4)
	rollback, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f1, b1}, []*types.CodeWikiRunRepository{f0, b0})
	require.NoError(t, err)
	require.Len(t, rollback, 4)
	for i, change := range joint {
		require.Equal(t, change.BeforeHash, rollback[i].AfterHash)
		require.Equal(t, change.AfterHash, rollback[i].BeforeHash)
	}
	noop, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f1, b1}, []*types.CodeWikiRunRepository{b1, f1})
	require.NoError(t, err)
	require.Empty(t, noop)
	// Scope filtering/removing a repository appears as deletions, not silence.
	removed, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f1, b1}, []*types.CodeWikiRunRepository{f1})
	require.NoError(t, err)
	require.Len(t, removed, 1)
	require.Equal(t, "deleted", removed[0].Kind)
	r.snapshots[f1.SnapshotID].FileCount++
	_, err = s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{f0}, []*types.CodeWikiRunRepository{f1})
	require.ErrorContains(t, err, "file count changed")
}

func TestCodeWikiTreeDiffRejectsSymlinksAndCancellation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
	_, err := hashCodeWikiTree(context.Background(), root)
	require.ErrorContains(t, err, "non-regular")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = hashCodeWikiTree(ctx, t.TempDir())
	require.ErrorIs(t, err, context.Canceled)
}

func TestCodeWikiJointDiffHandlesHistoricalExcludedRepositories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("export const api = 1\n"), 0600))
	snapshot := &types.CodeWikiSnapshot{ID: "ready", KnowledgeBaseID: "kb", RepositoryID: "front",
		CommitSHA: "sha", LocalPath: root, FileCount: 1}
	s := &codeWikiService{repo: codeWikiDiffRepository{snapshots: map[string]*types.CodeWikiSnapshot{"ready": snapshot}}}
	excluded := &types.CodeWikiRunRepository{RepositoryID: "front", Excluded: true, CommitSHA: "sha"}
	ready := &types.CodeWikiRunRepository{RepositoryID: "front", SnapshotID: "ready", CommitSHA: "sha"}

	changes, err := s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{excluded}, []*types.CodeWikiRunRepository{ready})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "added", changes[0].Kind)
	require.Equal(t, "api.ts", changes[0].Path)
	require.True(t, changes[0].ScopeChanged)
	require.Empty(t, changes[0].BeforeHash)
	require.NotEmpty(t, changes[0].AfterHash)

	changes, err = s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{ready}, []*types.CodeWikiRunRepository{excluded})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "deleted", changes[0].Kind)
	require.True(t, changes[0].ScopeChanged)

	changes, err = s.compareCodeWikiSnapshots(ctx, "kb", []*types.CodeWikiRunRepository{excluded}, []*types.CodeWikiRunRepository{excluded})
	require.NoError(t, err)
	require.Empty(t, changes)

	for _, entries := range [][]*types.CodeWikiRunRepository{
		{nil},
		{{RepositoryID: "front"}},
		{{Excluded: true}},
		{excluded, ready},
		{ready, excluded},
	} {
		_, err := s.compareCodeWikiSnapshots(ctx, "kb", entries, nil)
		require.Error(t, err, "malformed manifests must still fail")
	}
}

func TestCodeWikiSnapshotByteFingerprintRejectsChangedSource(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("original\n"), 0600))
	tree, err := hashCodeWikiTree(context.Background(), root)
	require.NoError(t, err)
	snapshot := &types.CodeWikiSnapshot{ID: "snapshot", KnowledgeBaseID: "kb", RepositoryID: "front", CommitSHA: "sha", FilterHash: "filters", LocalPath: root, FileCount: 1}
	s := &codeWikiService{repo: codeWikiDiffRepository{snapshots: map[string]*types.CodeWikiSnapshot{"snapshot": snapshot}}}
	manifest := []*types.CodeWikiRunRepository{{RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "sha"}}
	refs := map[string]types.CodeWikiFrozenReference{"front": {FilterHash: "filters", TreeHash: codeWikiTreeFingerprint(tree)}}
	require.NoError(t, s.verifyCodeWikiSnapshotReferences(context.Background(), "kb", manifest, refs))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("mutated \n"), 0600))
	require.ErrorContains(t, s.verifyCodeWikiSnapshotReferences(context.Background(), "kb", manifest, refs), "source bytes changed")
	require.ErrorContains(t, s.verifyCodeWikiSnapshotReferences(context.Background(), "kb", nil, refs), "manifest is incomplete")
}
