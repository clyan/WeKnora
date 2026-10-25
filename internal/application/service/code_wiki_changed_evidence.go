package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Prioritize actual changed source spans and share the bounded evidence budget
// across repositories. An unchanged side still receives its search allocation.
func (s *codeWikiService) changedCodeWikiEvidence(ctx context.Context, state *codeWikiRunState, page codeWikiPlannedPage, budget int) []types.CodeWikiEvidenceSpec {
	grouped := make(map[string][]types.CodeWikiSourceChange)
	for _, change := range state.cfg.SourceChanges {
		if types.IsCodeWikiFrontendCategory(page.Category) {
			matches := false
			for _, anchor := range page.SourcePaths {
				if change.RepositoryID == page.RepositoryID && change.Path == anchor {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		if change.AfterHash == "" || change.AfterStartLine == 0 {
			continue
		}
		if page.Category == types.CodeWikiPageCategoryRepository && page.RepositoryID != "" && change.RepositoryID != page.RepositoryID {
			continue
		}
		if _, ok := state.scopes[change.RepositoryID]; ok {
			grouped[change.RepositoryID] = append(grouped[change.RepositoryID], change)
		}
	}
	var ids []string
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var evidence []types.CodeWikiEvidenceSpec
	for offset := 0; len(evidence) < budget; offset++ {
		found := false
		for _, id := range ids {
			if offset >= len(grouped[id]) {
				continue
			}
			found = true
			change := grouped[id][offset]
			scope := state.scopes[id]
			if err := verifySnapshotFileLines(scope.LocalPath, change.Path, 1, 1); err != nil {
				continue
			}
			data, err := os.ReadFile(filepath.Join(scope.LocalPath, filepath.FromSlash(change.Path)))
			if err != nil || types.CodeWikiTextHash(string(data)) != change.AfterHash {
				continue
			}
			start := change.AfterStartLine - 2
			if start < 1 {
				start = 1
			}
			end := start + 30
			if count := strings.Count(string(data), "\n") + 1; end > count {
				end = count
			}
			quote, err := s.readSource(ctx, state, scope, change.Path, start, end)
			if err != nil {
				continue
			}
			evidence = append(evidence, types.CodeWikiEvidenceSpec{ID: fmt.Sprintf("E%d", len(evidence)+1), RepositoryID: id,
				SnapshotID: scope.SnapshotID, CommitSHA: scope.CommitSHA, Path: change.Path, StartLine: start, EndLine: end,
				Type: types.CodeWikiEvidenceTypeFile, Quote: truncateCodeWikiString(quote, 1200)})
			if len(evidence) >= budget {
				break
			}
		}
		if !found {
			break
		}
	}
	return evidence
}
