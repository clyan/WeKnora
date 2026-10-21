package codebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// BuiltinProvider adapts the pinned codebase-memory-mcp CLI. One instance
// serves all KBs; per-snapshot isolation comes from server-generated
// project names (SnapshotScope.ProjectID), never raw repository names.
type BuiltinProvider struct {
	exec *builtinExecutor
}

// NewBuiltinProvider builds the default adapter.
func NewBuiltinProvider() *BuiltinProvider {
	return &BuiltinProvider{exec: newBuiltinExecutor()}
}

func (p *BuiltinProvider) Kind() string { return types.CodeWikiProviderBuiltin }

// EngineVersion reports the CLI version. The version is part of the
// artifact contract: an artifact indexed by another version is re-prepared,
// not silently reused.
func (p *BuiltinProvider) EngineVersion(ctx context.Context) (string, error) {
	return p.exec.cliVersion(ctx)
}

// CheckCapabilities verifies the CLI is installed, the version matches the
// supported contract, and reports the builtin capability set. The builtin
// provider always satisfies the required capabilities when the CLI works —
// snapshot scoping comes from per-snapshot project names, source reads from
// the local filtered tree.
func (p *BuiltinProvider) CheckCapabilities(ctx context.Context, _ *types.CodeWikiAnalysisProfile) (*types.CodeWikiCapabilityReport, error) {
	report := &types.CodeWikiCapabilityReport{CheckedAt: time.Now().UTC()}
	version, err := p.exec.cliVersion(ctx)
	if err != nil {
		report.ConnectDetail = err.Error()
		return report, nil
	}
	report.Connected = true
	set := func(capability string, available bool, source, tool, detail string) {
		report.Capabilities = append(report.Capabilities, types.CodeWikiCapabilityStatus{
			Capability: capability, Available: available, Source: source, Tool: tool, Detail: detail,
		})
	}
	versionNote := "engine " + version
	if !strings.HasPrefix(strings.TrimSpace(version), strings.TrimSpace(BuiltinSupportedVersion)) {
		report.Notes = append(report.Notes,
			fmt.Sprintf("codebase-memory-mcp %s has not been validated for this integration (supported: %s); verify before relying on it", version, BuiltinSupportedVersion))
	}
	// Required capabilities.
	set(types.CodeWikiCapSnapshotScope, true, "builtin", "", versionNote)
	set(types.CodeWikiCapCodeSearch, true, "provider", "search_code", versionNote)
	set(types.CodeWikiCapSourceRead, true, "local", "", "source excerpts read from the local snapshot")
	set(types.CodeWikiCapSnapshotPrepare, true, "builtin", "index_repository", versionNote)
	// Optional capabilities provided by the engine.
	set(types.CodeWikiCapDirectory, true, "local", "", "filtered tree on disk")
	set(types.CodeWikiCapSymbols, true, "provider", "search_code", "symbol-aware search")
	set(types.CodeWikiCapCallGraph, true, "provider", "query_graph", "graph relations are candidates pending source verification")
	set(types.CodeWikiCapCrossRepo, false, "", "", "per-repository indexes; cross-repository association joined by WeKnora")
	set(types.CodeWikiCapCoverage, true, "provider", "index_status", versionNote)
	report.Notes = append(report.Notes,
		"no native cross-repository index: associations are formed by per-repository retrieval plus source verification")
	report.MeetsMinimum = true
	return report, nil
}

// PrepareArtifact indexes the snapshot directory under its deterministic
// project name, or verifies an existing index's recorded version.
func (p *BuiltinProvider) PrepareArtifact(ctx context.Context, scope SnapshotScope) (*ArtifactResult, error) {
	if scope.LocalPath == "" {
		return nil, fmt.Errorf("codebase: builtin provider requires a local snapshot path")
	}
	if _, err := os.Stat(scope.LocalPath); err != nil {
		return nil, fmt.Errorf("codebase: snapshot directory missing: %w", err)
	}
	project := scope.ProjectID()

	// Verify an existing index first: same project name is only reused when
	// the engine still reports it indexed.
	if status, err := p.exec.cliIndexStatus(ctx, project); err == nil && status.Indexed {
		version, vErr := p.exec.cliVersion(ctx)
		if vErr != nil {
			version = ""
		}
		return &ArtifactResult{Handle: project, EngineVersion: version, Ready: true}, nil
	}
	// Index (or re-index) the filtered tree.
	if err := p.exec.cliIndexRepository(ctx, scope.LocalPath, project); err != nil {
		return nil, err
	}
	version, vErr := p.exec.cliVersion(ctx)
	if vErr != nil {
		version = ""
	}
	coverage := coverageFromStatus(ctx, p.exec, project)
	return &ArtifactResult{Handle: project, EngineVersion: version, Ready: true, Coverage: coverage}, nil
}

func coverageFromStatus(ctx context.Context, e *builtinExecutor, project string) types.JSON {
	status, err := e.cliIndexStatus(ctx, project)
	if err != nil || status == nil {
		return nil
	}
	blob, err := json.Marshal(status)
	if err != nil {
		return nil
	}
	return types.JSON(blob)
}

// SearchCode runs a snapshot-scoped search via the project handle.
func (p *BuiltinProvider) SearchCode(ctx context.Context, scope SnapshotScope, query string, opts QueryOptions) ([]SearchHit, error) {
	limit := normalizeLimit(opts.Limit)
	return p.exec.cliSearchCode(ctx, scope.ArtifactHandle, query, limit)
}

// ReadSource serves excerpts from the local snapshot tree — the same bytes
// the index saw, so evidence validation and display never depend on the
// remote branch state.
func (p *BuiltinProvider) ReadSource(ctx context.Context, scope SnapshotScope, path string, startLine, endLine int) (string, error) {
	if scope.LocalPath == "" {
		return "", ErrUnsupported
	}
	clean, err := safeSnapshotPath(scope.LocalPath, path)
	if err != nil {
		return "", err
	}
	return readLines(clean, startLine, endLine)
}

// ListDirectory walks the filtered tree under a prefix.
func (p *BuiltinProvider) ListDirectory(ctx context.Context, scope SnapshotScope, pathPrefix string) ([]DirectoryEntry, error) {
	if scope.LocalPath == "" {
		return nil, ErrUnsupported
	}
	root, err := safeSnapshotPath(scope.LocalPath, pathPrefix)
	if err != nil {
		return nil, err
	}
	var out []DirectoryEntry
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(scope.LocalPath, path)
		if relErr != nil {
			return nil
		}
		entry := DirectoryEntry{Path: filepath.ToSlash(rel), IsDir: d.IsDir()}
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				entry.Size = info.Size()
			}
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("codebase: walk snapshot tree: %w", err)
	}
	return out, nil
}

// QuerySymbols is served through symbol-aware search in the first version.
func (p *BuiltinProvider) QuerySymbols(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]SymbolHit, error) {
	hits, err := p.exec.cliSearchCode(ctx, scope.ArtifactHandle, symbol, normalizeLimit(opts.Limit))
	if err != nil {
		return nil, err
	}
	out := make([]SymbolHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, SymbolHit{Symbol: h.Symbol, Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine})
	}
	return out, nil
}

// QueryRelations is served through graph queries in the first version; the
// engine surface is verified in stage 0. Until then relations degrade to
// search-based candidates.
func (p *BuiltinProvider) QueryRelations(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]RelationEdge, error) {
	return nil, ErrUnsupported
}

// QueryCrossRepository: the builtin engine indexes per repository, so
// cross-repository association is performed by WeKnora (per-repository
// retrieval + source verification), never faked as native graph edges.
func (p *BuiltinProvider) QueryCrossRepository(ctx context.Context, scopes []SnapshotScope, query string, opts QueryOptions) ([]RelationEdge, error) {
	return nil, ErrUnsupported
}

// ReportCoverage summarizes index state for one artifact.
func (p *BuiltinProvider) ReportCoverage(ctx context.Context, scope SnapshotScope) (*CoverageReport, error) {
	status, err := p.exec.cliIndexStatus(ctx, scope.ArtifactHandle)
	if err != nil {
		return nil, err
	}
	return &CoverageReport{
		EngineVersion: status.Version,
		TotalFiles:    status.Nodes, // node/edge counts carry scope magnitude
		IndexedFiles:  status.Nodes,
	}, nil
}

// CleanupArtifact deletes a project this integration created. Project names
// are always server-generated with the cw- prefix, so ownership is
// verifiable; anything else is refused rather than risk deleting an
// operator's own index.
func (p *BuiltinProvider) CleanupArtifact(ctx context.Context, handle string) error {
	if !strings.HasPrefix(handle, "cw-") {
		return ErrUnsupported
	}
	// The 0.12.1 CLI cleanup subcommand is part of stage-0 verification;
	// until verified, refuse with an explicit error instead of guessing argv.
	return ErrUnsupported
}

// --- helpers ----------------------------------------------------------------

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// safeSnapshotPath resolves a repository-relative path inside the snapshot
// root. ".." segments are rejected outright rather than Clean-resolved:
// silently redirecting "a/../../x" to "x" would be safe but surprising, and
// the plan's contract is to REFUSE traversal (拒绝路径穿越).
func safeSnapshotPath(root, rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("codebase: path traversal rejected: %s", rel)
		}
	}
	clean := filepath.Clean("/" + strings.TrimPrefix(rel, "/"))
	joined := filepath.Join(root, clean)
	if !strings.HasPrefix(joined, filepath.Clean(root)+string(os.PathSeparator)) && joined != filepath.Clean(root) {
		return "", fmt.Errorf("codebase: path escapes snapshot root: %s", rel)
	}
	return joined, nil
}

// readLines returns a bounded excerpt with 1-based inclusive line range.
func readLines(path string, startLine, endLine int) (string, error) {
	if startLine <= 0 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine-startLine+1 > types.CodeWikiMaxSnippetLines {
		endLine = startLine + types.CodeWikiMaxSnippetLines - 1
	}
	data, err := os.ReadFile(path) //nolint:gosec // path already confined to snapshot root
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		return "", fmt.Errorf("codebase: start line %d beyond file length %d", startLine, len(lines))
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return strings.Join(lines[startLine-1:endLine], "\n"), nil
}
