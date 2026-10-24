package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Compare the filtered immutable trees, not Git ancestry or current branch
// tips. This also handles unrelated branches, removed repositories and filters.
func (s *codeWikiService) compareCodeWikiSnapshots(ctx context.Context, kbID string,
	before, after []*types.CodeWikiRunRepository,
) ([]types.CodeWikiSourceChange, error) {
	beforeSnapshots, afterSnapshots := map[string]*types.CodeWikiSnapshot{}, map[string]*types.CodeWikiSnapshot{}
	load := func(entries []*types.CodeWikiRunRepository, snapshots map[string]*types.CodeWikiSnapshot) (map[string]map[string]string, error) {
		out := make(map[string]map[string]string, len(entries))
		seen := make(map[string]bool, len(entries))
		for _, entry := range entries {
			if entry == nil || entry.RepositoryID == "" {
				return nil, fmt.Errorf("incomplete repository manifest")
			}
			if seen[entry.RepositoryID] {
				return nil, fmt.Errorf("duplicate repository in manifest")
			}
			seen[entry.RepositoryID] = true
			// Older published runs retain excluded repositories for disclosure.
			// They contributed no source tree; becoming ready adds their scope.
			if entry.Excluded {
				continue
			}
			if entry.SnapshotID == "" {
				return nil, fmt.Errorf("incomplete repository manifest")
			}
			snapshot, err := s.repo.GetSnapshot(ctx, kbID, entry.SnapshotID)
			if err != nil {
				return nil, err
			}
			if snapshot.RepositoryID != entry.RepositoryID || snapshot.CommitSHA != entry.CommitSHA {
				return nil, fmt.Errorf("snapshot does not match frozen repository identity")
			}
			tree, err := hashCodeWikiTree(ctx, snapshot.LocalPath)
			if err != nil {
				return nil, err
			}
			if len(tree) != snapshot.FileCount {
				return nil, fmt.Errorf("snapshot file count changed: %s", snapshot.ID)
			}
			out[entry.RepositoryID] = tree
			snapshots[entry.RepositoryID] = snapshot
		}
		return out, nil
	}
	oldTrees, err := load(before, beforeSnapshots)
	if err != nil {
		return nil, err
	}
	newTrees, err := load(after, afterSnapshots)
	if err != nil {
		return nil, err
	}
	var changes []types.CodeWikiSourceChange
	repositories := make(map[string]bool)
	for id := range oldTrees {
		repositories[id] = true
	}
	for id := range newTrees {
		repositories[id] = true
	}
	for id := range repositories {
		paths := make(map[string]bool)
		for path := range oldTrees[id] {
			paths[path] = true
		}
		for path := range newTrees[id] {
			paths[path] = true
		}
		for path := range paths {
			oldHash, newHash := oldTrees[id][path], newTrees[id][path]
			if oldHash == newHash {
				continue
			}
			kind := "modified"
			if oldHash == "" {
				kind = "added"
			} else if newHash == "" {
				kind = "deleted"
			}
			oldSnapshot, newSnapshot := beforeSnapshots[id], afterSnapshots[id]
			beforeLine, afterLine := codeWikiFirstChangedLines(oldSnapshot, newSnapshot, path)
			scopeChanged := oldSnapshot == nil || newSnapshot == nil || oldSnapshot.FilterHash != newSnapshot.FilterHash
			changes = append(changes, types.CodeWikiSourceChange{RepositoryID: id, Path: path,
				Kind: kind, BeforeHash: oldHash, AfterHash: newHash, BeforeStartLine: beforeLine, AfterStartLine: afterLine, ScopeChanged: scopeChanged})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].RepositoryID != changes[j].RepositoryID {
			return changes[i].RepositoryID < changes[j].RepositoryID
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func codeWikiFirstChangedLines(before, after *types.CodeWikiSnapshot, path string) (int, int) {
	read := func(snapshot *types.CodeWikiSnapshot) []string {
		if snapshot == nil {
			return nil
		}
		file := filepath.Join(snapshot.LocalPath, filepath.FromSlash(path))
		info, err := os.Stat(file)
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil || strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		return strings.Split(string(data), "\n")
	}
	old, next := read(before), read(after)
	i := 0
	for i < len(old) && i < len(next) && old[i] == next[i] {
		i++
	}
	oldLine, newLine := 0, 0
	if len(old) > 0 {
		oldLine = i + 1
		if oldLine > len(old) {
			oldLine = len(old)
		}
	}
	if len(next) > 0 {
		newLine = i + 1
		if newLine > len(next) {
			newLine = len(next)
		}
	}
	return oldLine, newLine
}

func hashCodeWikiTree(ctx context.Context, root string) (map[string]string, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("snapshot root must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("snapshot root is missing or unsafe: %s", root)
	}
	files := make(map[string]string)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular snapshot file: %s", path)
		}
		if len(files) >= 100000 {
			return fmt.Errorf("snapshot diff exceeds file limit; no partial diff is accepted")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	return files, err
}

func codeWikiTreeFingerprint(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", path, files[path])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *codeWikiService) verifyCodeWikiSnapshotReferences(ctx context.Context, kbID string, entries []*types.CodeWikiRunRepository, refs map[string]types.CodeWikiFrozenReference) error {
	if len(refs) == 0 {
		return nil
	} // Legacy runs have no immutable tree hash.
	if len(entries) != len(refs) {
		return fmt.Errorf("frozen snapshot manifest is incomplete")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry == nil || entry.Excluded || seen[entry.RepositoryID] {
			return fmt.Errorf("invalid frozen snapshot manifest")
		}
		seen[entry.RepositoryID] = true
		reference, ok := refs[entry.RepositoryID]
		if !ok || reference.TreeHash == "" {
			return fmt.Errorf("frozen source tree hash is missing")
		}
		snapshot, err := s.repo.GetSnapshot(ctx, kbID, entry.SnapshotID)
		if err != nil {
			return err
		}
		if snapshot.RepositoryID != entry.RepositoryID || snapshot.CommitSHA != entry.CommitSHA || snapshot.FilterHash != reference.FilterHash {
			return fmt.Errorf("immutable snapshot identity changed")
		}
		files, err := hashCodeWikiTree(ctx, snapshot.LocalPath)
		if err != nil {
			return err
		}
		if len(files) != snapshot.FileCount || codeWikiTreeFingerprint(files) != reference.TreeHash {
			return fmt.Errorf("immutable snapshot source bytes changed: %s", entry.RepositoryID)
		}
	}
	return nil
}
