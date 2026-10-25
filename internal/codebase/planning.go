package codebase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PlanningStructureProvider is optional: discovery uses indexed locations, not
// source excerpts or inferred business facts. A missing capability is explicit.
type PlanningStructureProvider interface {
	PlanningStructure(context.Context, SnapshotScope, string, QueryOptions) ([]PlanningHint, error)
}

type PlanningHint struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Target     string `json:"target,omitempty"`
	TargetPath string `json:"target_path,omitempty"`
}

// PlanningStructure pages declarations and usage separately. Unlike the bounded
// architecture overview, this surface does not silently omit remaining nodes.
func (p *BuiltinProvider) PlanningStructure(ctx context.Context, scope SnapshotScope, stream string, opts QueryOptions) ([]PlanningHint, error) {
	if scope.KnowledgeBaseID == "" || scope.SnapshotID == "" || scope.RepositoryID == "" || scope.LocalPath == "" || scope.ArtifactHandle != scope.ProjectID() {
		return nil, fmt.Errorf("codebase: planning requires an owned frozen snapshot")
	}
	if opts.Offset < 0 || opts.Limit < 1 || opts.Limit > 256 {
		return nil, fmt.Errorf("codebase: invalid planning page")
	}
	var query string
	switch stream {
	case "symbols":
		query = `MATCH (n) WHERE n.file_path IS NOT NULL AND (n:Module OR n:Function OR n:Method OR n:Class OR n:Variable OR n:Route OR n:Interface OR n:Type) RETURN labels(n) AS kind, n.name AS name, n.file_path AS path, n.start_line AS line, n.qualified_name AS identity ORDER BY path, line, identity`
	case "relations":
		query = `MATCH (a)-[r:IMPORTS|CALLS|USAGE|HTTP_CALLS|HANDLES]->(b) WHERE a.file_path IS NOT NULL AND b.file_path IS NOT NULL RETURN type(r) AS kind, a.name AS name, a.file_path AS path, a.start_line AS line, b.name AS target, b.file_path AS target_path, a.qualified_name AS identity, b.qualified_name AS target_identity ORDER BY path, identity, kind, target_path, target_identity`
	default:
		return nil, fmt.Errorf("codebase: unknown planning stream")
	}
	query += fmt.Sprintf(" SKIP %d LIMIT %d", opts.Offset, opts.Limit)
	var result struct {
		Columns   []string            `json:"columns"`
		Rows      [][]json.RawMessage `json:"rows"`
		HasMore   bool                `json:"has_more"`
		Truncated bool                `json:"truncated"`
	}
	if err := p.exec.runJSON(ctx, builtinRunTimeout, &result, "cli", "query_graph", "--project", scope.ArtifactHandle, "--query", query, "--format", "json", "--max-rows", strconv.Itoa(opts.Limit), "--max-output-tokens", "262144"); err != nil {
		return nil, err
	}
	if len(result.Rows) > opts.Limit || result.HasMore || result.Truncated {
		return nil, fmt.Errorf("codebase: planning query was truncated; no partial page accepted")
	}
	columns := map[string]bool{}
	for _, col := range result.Columns {
		columns[col] = true
	}
	for _, key := range []string{"kind", "name", "path", "line", "identity"} {
		if !columns[key] {
			return nil, fmt.Errorf("codebase: planning query missing %s", key)
		}
	}
	var hints []PlanningHint
	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return nil, fmt.Errorf("codebase: malformed planning row")
		}
		cells := tableRow(result.Columns, row)
		var hint PlanningHint
		for key, target := range map[string]*string{"kind": &hint.Kind, "name": &hint.Name, "path": &hint.Path, "target": &hint.Target, "target_path": &hint.TargetPath} {
			if raw := cells[key]; len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, target); err != nil {
					return nil, err
				}
			}
		}
		var identity string
		if err := json.Unmarshal(cells["identity"], &identity); err != nil || !strings.HasPrefix(identity, scope.ArtifactHandle+".") {
			return nil, fmt.Errorf("codebase: planning returned another project")
		}
		if stream == "relations" {
			var target string
			if err := json.Unmarshal(cells["target_identity"], &target); err != nil || !strings.HasPrefix(target, scope.ArtifactHandle+".") {
				return nil, fmt.Errorf("codebase: planning target outside project")
			}
		}
		value := strings.Trim(string(cells["line"]), "\"")
		var err error
		hint.Line, err = strconv.Atoi(value)
		if err != nil || hint.Line < 1 {
			continue
		}
		// Paths are validated against the authorized inventory by the consumer.
		hints = append(hints, hint)
	}
	// Preserve row count for pagination, including location-less rows. Such rows
	// are empty hints and never become candidate evidence.
	for len(hints) < len(result.Rows) {
		hints = append(hints, PlanningHint{})
	}
	return hints, nil
}
