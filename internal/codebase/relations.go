package codebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

func (p *BuiltinProvider) queryRelations(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]RelationEdge, error) {
	if scope.KnowledgeBaseID == "" || scope.SnapshotID == "" || scope.RepositoryID == "" || scope.LocalPath == "" || scope.ArtifactHandle != scope.ProjectID() {
		return nil, fmt.Errorf("codebase: call query requires an owned frozen snapshot")
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("codebase: negative call query offset")
	}
	rows, err := p.exec.cliCallRelations(ctx, scope.ArtifactHandle, symbol, normalizeLimit(opts.Limit), opts.Offset)
	if err != nil {
		return nil, err
	}
	var edges []RelationEdge
	for _, cells := range rows {
		var edge RelationEdge
		for key, dest := range map[string]*string{"kind": &edge.Kind, "from_symbol": &edge.FromSymbol, "from_path": &edge.FromPath, "to_symbol": &edge.ToSymbol, "to_path": &edge.ToPath} {
			if err := json.Unmarshal(cells[key], dest); err != nil {
				return nil, fmt.Errorf("codebase: invalid call graph %s: %w", key, err)
			}
		}
		if edge.Kind != "CALLS" || !strings.HasPrefix(edge.FromSymbol, scope.ArtifactHandle+".") || !strings.HasPrefix(edge.ToSymbol, scope.ArtifactHandle+".") {
			return nil, fmt.Errorf("codebase: call graph returned an unexpected relationship or project")
		}
		// Engine 0.12.1 serializes graph property integers as JSON strings.
		lines := []*int{&edge.StartLine, &edge.EndLine, &edge.ToStartLine, &edge.ToEndLine}
		for i, key := range []string{"from_line", "from_end", "to_line", "to_end"} {
			raw := cells[key]
			var value string
			if len(raw) > 0 && raw[0] == '"' {
				if err := json.Unmarshal(raw, &value); err != nil {
					return nil, err
				}
			} else {
				value = string(raw)
			}
			if value == "null" {
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("codebase: invalid call graph %s: %w", key, err)
			}
			*lines[i] = n
		}
		// Unresolved/external endpoints have no verifiable source. Never turn
		// them into invented line-1 citations or third-party implementation facts.
		if edge.FromPath == "" || edge.ToPath == "" || edge.StartLine < 1 || edge.ToStartLine < 1 {
			continue
		}
		for _, endpoint := range []struct {
			path  string
			start int
			end   *int
		}{
			{edge.FromPath, edge.StartLine, &edge.EndLine}, {edge.ToPath, edge.ToStartLine, &edge.ToEndLine},
		} {
			if err := validateCallEndpoint(scope.LocalPath, endpoint.path, endpoint.start, endpoint.end); err != nil {
				return nil, err
			}
		}
		edge.Kind = "call"
		edge.FromRepositoryID, edge.ToRepositoryID = scope.RepositoryID, scope.RepositoryID
		edges = append(edges, edge)
	}
	return edges, nil
}

func validateCallEndpoint(root, path string, start int, end *int) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("codebase: absolute call source path rejected")
	}
	full, err := safeSnapshotPath(root, path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("codebase: call source escapes snapshot")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	count := len(strings.Split(string(data), "\n"))
	if *end == 0 {
		*end = start
	}
	if *end < start || start > count || *end > count {
		return fmt.Errorf("codebase: call source line range does not match snapshot: %s", path)
	}
	if *end-start+1 > types.CodeWikiMaxSnippetLines {
		*end = start + types.CodeWikiMaxSnippetLines - 1
	}
	return nil
}
