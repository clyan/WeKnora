package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiSourceBrowsingPaginationAndConfinement(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 205; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.ts", i)), []byte("source"), 0600))
	}
	first, cursor, err := readCodeWikiSourceDirectory(context.Background(), root, "", "")
	require.NoError(t, err)
	require.Len(t, first, 200)
	require.Equal(t, "file-199.ts", cursor)
	second, cursor, err := readCodeWikiSourceDirectory(context.Background(), root, "", cursor)
	require.NoError(t, err)
	require.Len(t, second, 5)
	require.Empty(t, cursor)
	require.Equal(t, "file-200.ts", second[0].Path)
	for _, prefix := range []string{"..", "a/../b", "/absolute", "a\\b"} {
		_, _, err := readCodeWikiSourceDirectory(context.Background(), root, prefix, "")
		require.Error(t, err)
	}
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "escape")))
	_, _, err = readCodeWikiSourceDirectory(context.Background(), root, "escape", "")
	require.ErrorContains(t, err, "symlink")
}

func TestCodeWikiSourceVerificationChecksFullRangeAndSnapshotIdentity(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("line one\nline two"), 0600))
	require.NoError(t, verifySnapshotFileLines(root, "api.ts", 1, 2))
	for _, bounds := range [][2]int{{0, 1}, {2, 1}, {1, 3}} {
		require.Error(t, verifySnapshotFileLines(root, "api.ts", bounds[0], bounds[1]))
	}
	require.Error(t, verifySnapshotFileLines(root, "../api.ts", 1, 1))
	require.NoError(t, os.Symlink(filepath.Join(root, "api.ts"), filepath.Join(root, "link.ts")))
	require.Error(t, verifySnapshotFileLines(root, "link.ts", 1, 1))
	s := &codeWikiService{}
	state := &codeWikiRunState{scopes: map[string]codebase.SnapshotScope{"front": {RepositoryID: "front", SnapshotID: "target", CommitSHA: "target-sha", LocalPath: root}}}
	_, problems := s.validateCitations(context.Background(), state, "claim [E1]", []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "old", CommitSHA: "old-sha", Path: "api.ts", StartLine: 1, EndLine: 1}})
	require.NotEmpty(t, problems)
	_, problems = s.validateCitations(context.Background(), state, "invented [E99]", nil)
	require.NotEmpty(t, problems)
}
