package codebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type npmSnapshotPackage struct {
	scope        SnapshotScope
	path         string
	name         string
	content      string
	dependencies map[string]string
}

// npmSnapshotPackages includes nested workspace packages, not just the repository
// root. Names come from package.json, never from repository display names.
func (p *BuiltinProvider) npmSnapshotPackages(ctx context.Context, scope SnapshotScope) ([]npmSnapshotPackage, error) {
	entries, err := p.ListDirectory(ctx, scope, "")
	if err != nil {
		return nil, err
	}
	var packages []npmSnapshotPackage
	for _, entry := range entries {
		if entry.IsDir || filepath.Base(entry.Path) != "package.json" || entry.Size > 256<<10 {
			continue
		}
		path, err := safeSnapshotPath(scope.LocalPath, entry.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var manifest struct {
			Name                 string            `json:"name"`
			Dependencies         map[string]string `json:"dependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Name == "" {
			continue
		}
		deps := make(map[string]string)
		// Development tools are not business-package relationships. Only
		// runtime/peer/optional dependencies whose source was supplied qualify.
		for _, group := range []map[string]string{manifest.Dependencies, manifest.PeerDependencies, manifest.OptionalDependencies} {
			for name, version := range group {
				deps[name] = version
			}
		}
		packages = append(packages, npmSnapshotPackage{scope, entry.Path, manifest.Name, string(data), deps})
	}
	return packages, nil
}

func manifestNameLine(content, name string) int {
	encoded, _ := json.Marshal(name)
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, string(encoded)) {
			return i + 1
		}
	}
	return 1
}

func (p *BuiltinProvider) queryCrossRepository(ctx context.Context, scopes []SnapshotScope, opts QueryOptions) ([]RelationEdge, error) {
	if len(scopes) < 2 {
		return nil, nil
	}
	// Deterministic ordering also makes the evidence budget reproducible.
	scopes = append([]SnapshotScope(nil), scopes...)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].RepositoryID < scopes[j].RepositoryID })
	kb := scopes[0].KnowledgeBaseID
	var packages []npmSnapshotPackage
	for _, scope := range scopes {
		if kb == "" || scope.KnowledgeBaseID != kb || scope.ArtifactHandle != scope.ProjectID() {
			return nil, fmt.Errorf("codebase: cross-repository query requires owned snapshots from one knowledge base")
		}
		found, err := p.npmSnapshotPackages(ctx, scope)
		if err != nil {
			return nil, err
		}
		packages = append(packages, found...)
	}
	for _, scope := range scopes {
		var targets []string
		for _, other := range scopes {
			if other.RepositoryID != scope.RepositoryID {
				targets = append(targets, other.ArtifactHandle)
			}
		}
		if err := p.exec.cliLinkRepositories(ctx, scope, targets); err != nil {
			return nil, fmt.Errorf("codebase: native cross-repository linking for %s: %w", scope.RepositoryID, err)
		}
	}
	limit := normalizeLimit(opts.Limit)
	edges, err := p.npmPackageRelations(ctx, packages, limit)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if len(edges) >= limit {
			break
		}
		rows, err := p.exec.cliCrossRelations(ctx, scope.ArtifactHandle, limit-len(edges))
		if err != nil {
			return nil, err
		}
		for _, cells := range rows {
			var edge RelationEdge
			_ = json.Unmarshal(cells["kind"], &edge.Kind)
			_ = json.Unmarshal(cells["from_symbol"], &edge.FromSymbol)
			_ = json.Unmarshal(cells["to_symbol"], &edge.ToSymbol)
			_ = json.Unmarshal(cells["from_path"], &edge.FromPath)
			_ = json.Unmarshal(cells["to_path"], &edge.ToPath)
			_ = json.Unmarshal(cells["from_line"], &edge.StartLine)
			_ = json.Unmarshal(cells["to_line"], &edge.ToStartLine)
			for _, candidate := range scopes {
				if strings.HasPrefix(edge.FromSymbol, candidate.ArtifactHandle+".") {
					edge.FromRepositoryID = candidate.RepositoryID
				}
				if strings.HasPrefix(edge.ToSymbol, candidate.ArtifactHandle+".") {
					edge.ToRepositoryID = candidate.RepositoryID
				}
			}
			if edge.FromRepositoryID == "" || edge.ToRepositoryID == "" || edge.FromRepositoryID == edge.ToRepositoryID || edge.FromPath == "" || edge.ToPath == "" {
				continue
			}
			if edge.StartLine < 1 {
				edge.StartLine = 1
			}
			edge.EndLine = edge.StartLine
			if edge.ToStartLine < 1 {
				edge.ToStartLine = 1
			}
			edge.ToEndLine = edge.ToStartLine
			edges = append(edges, edge)
		}
	}
	return edges, nil
}

func (p *BuiltinProvider) npmPackageRelations(ctx context.Context, packages []npmSnapshotPackage, limit int) ([]RelationEdge, error) {
	var edges []RelationEdge
	for _, from := range packages {
		for _, to := range packages {
			if from.scope.RepositoryID == to.scope.RepositoryID {
				continue
			}
			if _, ok := from.dependencies[to.name]; !ok {
				continue
			}
			line, targetLine := manifestNameLine(from.content, to.name), manifestNameLine(to.content, to.name)
			edge := RelationEdge{Kind: "package_dependency", FromRepositoryID: from.scope.RepositoryID,
				ToRepositoryID: to.scope.RepositoryID, FromSymbol: from.name, ToSymbol: to.name,
				FromPath: from.path, ToPath: to.path, StartLine: line, EndLine: line,
				ToStartLine: targetLine, ToEndLine: targetLine, ExternalTarget: to.name}
			edges = append(edges, edge)
			if len(edges) >= limit {
				return edges, nil
			}
		}
	}
	// An import is evidence of package use, not proof of a particular exported
	// function's implementation or of installed-version equality.
	base := append([]RelationEdge(nil), edges...)
	for _, dep := range base {
		var scope SnapshotScope
		for _, pkg := range packages {
			if pkg.scope.RepositoryID == dep.FromRepositoryID {
				scope = pkg.scope
				break
			}
		}
		hits, err := p.SearchCode(ctx, scope, dep.ToSymbol, QueryOptions{Limit: 3})
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if filepath.Base(hit.Path) == "package.json" {
				continue
			}
			text, err := p.ReadSource(ctx, scope, hit.Path, hit.StartLine, hit.EndLine)
			if err != nil {
				continue
			}
			for i, line := range strings.Split(text, "\n") {
				if !strings.Contains(line, "import") && !strings.Contains(line, "export") && !strings.Contains(line, "require(") {
					continue
				}
				matched := false
				for _, quote := range []string{"\"", "'"} {
					if strings.Contains(line, quote+dep.ToSymbol+quote) || strings.Contains(line, quote+dep.ToSymbol+"/") {
						matched = true
					}
				}
				if !matched {
					continue
				}
				edge := dep
				edge.Kind, edge.FromPath, edge.StartLine = "package_import", hit.Path, hit.StartLine+i
				edge.EndLine = edge.StartLine
				edges = append(edges, edge)
				if len(edges) >= limit {
					return edges, nil
				}
				break
			}
		}
	}
	return edges, nil
}
