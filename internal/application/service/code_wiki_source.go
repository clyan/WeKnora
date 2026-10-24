package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

func (s *codeWikiService) ListSourcePages(ctx context.Context, kbID, repositoryID, snapshotID, path, after string) (*types.CodeWikiSourcePages, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetRepository(ctx, kbID, repositoryID); err != nil {
		return nil, err
	}
	snapshot, err := s.repo.GetSnapshot(ctx, kbID, snapshotID)
	if err != nil {
		return nil, err
	}
	if snapshot.RepositoryID != repositoryID {
		return nil, fmt.Errorf("snapshot does not belong to this repository")
	}
	if err := verifySnapshotFileLines(snapshot.LocalPath, path, 1, 1); err != nil {
		return nil, err
	}
	return s.repo.ListSourcePages(ctx, kbID, repositoryID, snapshotID, path, after)
}

func (s *codeWikiService) ListSourceDirectory(ctx context.Context, kbID, repositoryID, snapshotID, prefix, after string) (*types.CodeWikiSourceDirectory, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	binding, err := s.repo.GetRepository(ctx, kbID, repositoryID)
	if err != nil {
		return nil, err
	}
	if snapshotID == "" {
		if binding.LastSnapshotID == nil {
			return nil, fmt.Errorf("repository has no synced snapshot")
		}
		snapshotID = *binding.LastSnapshotID
	}
	snapshot, err := s.repo.GetSnapshot(ctx, kbID, snapshotID)
	if err != nil {
		return nil, err
	}
	if snapshot.RepositoryID != repositoryID {
		return nil, fmt.Errorf("snapshot does not belong to this repository")
	}
	entries, next, err := readCodeWikiSourceDirectory(ctx, snapshot.LocalPath, prefix, after)
	if err != nil {
		return nil, err
	}
	return &types.CodeWikiSourceDirectory{SnapshotID: snapshot.ID, CommitSHA: snapshot.CommitSHA, Prefix: prefix, Entries: entries, NextCursor: next}, nil
}

func readCodeWikiSourceDirectory(ctx context.Context, root, prefix, after string) ([]types.CodeWikiSourceEntry, string, error) {
	if !filepath.IsAbs(root) || strings.ContainsAny(prefix, "\\\x00") || filepath.IsAbs(prefix) {
		return nil, "", fmt.Errorf("invalid source directory")
	}
	if prefix != "" {
		for _, segment := range strings.Split(prefix, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return nil, "", fmt.Errorf("invalid source prefix")
			}
		}
	}
	path := filepath.Join(root, filepath.FromSlash(prefix))
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	// Reject symlink traversal, including links that happen to point inside.
	if realPath != filepath.Join(realRoot, filepath.FromSlash(prefix)) {
		return nil, "", fmt.Errorf("source directory contains a symlink")
	}
	children, err := os.ReadDir(realPath)
	if err != nil {
		return nil, "", err
	}
	entries := make([]types.CodeWikiSourceEntry, 0, 200)
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if child.Name() <= after || child.Type()&os.ModeSymlink != 0 {
			continue
		}
		if len(entries) == 200 {
			return entries, entries[len(entries)-1].Name, nil
		}
		info, err := child.Info()
		if err != nil {
			return nil, "", err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		entries = append(entries, types.CodeWikiSourceEntry{Name: child.Name(), Path: filepath.ToSlash(filepath.Join(prefix, child.Name())), IsDirectory: child.IsDir(), Size: info.Size()})
	}
	return entries, "", nil
}
