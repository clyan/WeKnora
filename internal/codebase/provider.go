// Package codebase adapts pluggable code-analysis providers behind one
// narrow interface. The business layer (code wiki service) depends only on
// CodeAnalysisProvider — never on a specific MCP tool name, graph query
// syntax or CLI argv — so the builtin codebase-memory CLI and custom MCP
// services are interchangeable to the extent their capabilities allow.
//
// Two adapters ship in the first version:
//
//   - builtin (builtin_codebase): drives the pinned codebase-memory-mcp
//     executable through a controlled CLI/JSON surface (exec.CommandContext
//     with an argv array — never a shell).
//   - custom MCP (custom_mcp): reuses the tenant's MCP services (SSE / HTTP
//     Streamable) with declarative capability→tool mappings.
//
// Capability semantics follow docs/CODE_WIKI_IMPLEMENTATION_PLAN.md §4.3:
// required capabilities (snapshot scoping, code search, source read,
// snapshot preparation) block generation when missing; optional ones
// (directory, symbols, call graph, native cross-repo, coverage) degrade the
// analysis with explicit disclosure in the generated documents.
package codebase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ErrUnsupported reports a capability the provider does not implement
// (unmapped tool / CLI subcommand absent). Callers treat it as a degrade,
// not a failure, after consulting the capability report.
var ErrUnsupported = errors.New("codebase: capability not supported by provider")

// ErrArtifactNotReady reports a snapshot whose analysis artifact exists but
// is still preparing. The sync flow waits/retries instead of failing.
var ErrArtifactNotReady = errors.New("codebase: analysis artifact not ready")

// ErrScopeRejected reports a provider that cannot pin queries to the
// requested repository/commit/filter scope. Providers whose tools only
// accept mutable branch refs are unusable for publishable documents.
var ErrScopeRejected = errors.New("codebase: provider cannot scope queries to the requested snapshot")

// SnapshotScope pins every provider query to one analyzed snapshot. The
// server generates all identifiers; raw repository names never appear, so
// same-named repositories in different KBs cannot collide.
type SnapshotScope struct {
	// KnowledgeBaseID scopes the artifact to one KB (isolation boundary).
	KnowledgeBaseID string
	// SnapshotID is the code_wiki_snapshots.id (server UUID).
	SnapshotID string
	// RepositoryID is the code_wiki_repositories.id (server UUID).
	RepositoryID string
	// CommitSHA is the frozen commit the snapshot downloaded.
	CommitSHA string
	// FilterHash identifies the include/exclude configuration.
	FilterHash string
	// LocalPath is the filtered tree on WeKnora's disk. Builtin/shared-dir
	// providers read it; remote services never receive it unless the
	// configured delivery method explicitly allows it.
	LocalPath string
	// ArtifactHandle is the provider's opaque handle from PrepareArtifact
	// (graph project name, index id, …).
	ArtifactHandle string
}

// ProjectID returns the server-generated index project identifier for the
// scope. Deterministic so re-preparation under the same profile version
// converges on one project instead of accumulating duplicates. Hash-based
// (not prefix-truncated): any differing UUID bit changes the identifier,
// so same-named repositories and near-identical KB ids can never collide.
func (s SnapshotScope) ProjectID() string {
	return "cw-" + shortID(s.KnowledgeBaseID) + "-" + shortID(s.SnapshotID)
}

// shortID derives a 16-hex-char token from an id via SHA-256, keeping
// generated project names well under provider length limits and free of
// provider-hostile characters.
func shortID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

// ArtifactResult is the outcome of preparing (or verifying) one snapshot's
// analysis artifact.
type ArtifactResult struct {
	// Handle is the provider's opaque artifact handle to persist.
	Handle string
	// EngineVersion is the provider/engine version recorded for the artifact.
	EngineVersion string
	// Ready reports whether the artifact is queryable now. False with nil
	// error means still preparing (ErrArtifactNotReady semantics).
	Ready bool
	// Coverage carries languages/file coverage when the provider reports it.
	Coverage types.JSON
}

// SearchHit is one standardized code-search result. Providers map their
// native results into this shape; Path is repository-relative.
type SearchHit struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

// SymbolHit is one symbol definition/reference hit.
type SymbolHit struct {
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// RelationEdge is one call/dependency relation candidate. Relations from
// providers are CANDIDATES for cross-repository association — the
// generation pipeline must confirm them against source before describing
// them as established behavior.
type RelationEdge struct {
	FromRepositoryID string `json:"from_repository_id,omitempty"`
	ToRepositoryID   string `json:"to_repository_id,omitempty"`
	Kind             string `json:"kind"` // call | http_call | grpc_call | import | …
	FromSymbol       string `json:"from_symbol,omitempty"`
	FromPath         string `json:"from_path,omitempty"`
	ToSymbol         string `json:"to_symbol,omitempty"`
	ToPath           string `json:"to_path,omitempty"`
	// ExternalTarget carries an out-of-repository target (URL, route,
	// service name) for cross-repository association candidates.
	ExternalTarget string `json:"external_target,omitempty"`
	StartLine      int    `json:"start_line,omitempty"`
	EndLine        int    `json:"end_line,omitempty"`
	ToStartLine    int    `json:"to_start_line,omitempty"`
	ToEndLine      int    `json:"to_end_line,omitempty"`
}

// DirectoryEntry is one node of a repository's (filtered) file tree.
type DirectoryEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// CoverageReport summarizes what the provider actually indexed.
type CoverageReport struct {
	EngineVersion string   `json:"engine_version,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	IndexedFiles  int      `json:"indexed_files,omitempty"`
	TotalFiles    int      `json:"total_files,omitempty"`
	UnparsedFiles []string `json:"unparsed_files,omitempty"`
}

// QueryOptions bounds provider queries. The generation pipeline always sets
// limits; nothing may pour an entire repository into a model context.
type QueryOptions struct {
	Limit  int
	Offset int
}

// CodeAnalysisProvider is the unified adapter interface. Implementations
// MUST enforce snapshot scoping on every query: results outside the frozen
// repository/commit/filter scope are a correctness bug, because published
// documents cite them as evidence.
type CodeAnalysisProvider interface {
	// Kind returns the provider kind constant (types.CodeWikiProvider*).
	Kind() string

	// EngineVersion reports the engine/CLI/service version. Used for the
	// version contract: artifacts record the version they were built with
	// and must not silently mix across upgrades.
	EngineVersion(ctx context.Context) (string, error)

	// CheckCapabilities runs the layered capability check:
	// connectivity → mapping validity → snapshot preparation support →
	// per-capability availability. The report distinguishes "connected"
	// from "usable"; connection success alone must never be reported as
	// analysis capability.
	CheckCapabilities(ctx context.Context, profile *types.CodeWikiAnalysisProfile) (*types.CodeWikiCapabilityReport, error)

	// PrepareArtifact creates or verifies the analysis artifact for one
	// snapshot under this provider, returning its handle. Preparing an
	// already-prepared snapshot verifies the recorded scope and version
	// instead of blindly reusing a same-named project.
	PrepareArtifact(ctx context.Context, scope SnapshotScope) (*ArtifactResult, error)

	// SearchCode runs a snapshot-scoped code search. Required capability.
	SearchCode(ctx context.Context, scope SnapshotScope, query string, opts QueryOptions) ([]SearchHit, error)

	// ReadSource returns a bounded source excerpt. Providers that expose a
	// source tool serve it directly; otherwise the service falls back to
	// reading WeKnora's local snapshot copy and this method returns
	// ErrUnsupported.
	ReadSource(ctx context.Context, scope SnapshotScope, path string, startLine, endLine int) (string, error)

	// ListDirectory returns the (filtered) tree structure. Optional.
	ListDirectory(ctx context.Context, scope SnapshotScope, pathPrefix string) ([]DirectoryEntry, error)

	// QuerySymbols searches symbol definitions/references. Optional.
	QuerySymbols(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]SymbolHit, error)

	// QueryRelations returns call/dependency relation candidates. Optional.
	QueryRelations(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]RelationEdge, error)

	// QueryCrossRepository returns relation candidates across the frozen
	// repository set (native cross-repo capability). Optional; when
	// unsupported the service performs per-repository searches and joins
	// them itself.
	QueryCrossRepository(ctx context.Context, scopes []SnapshotScope, query string, opts QueryOptions) ([]RelationEdge, error)

	// ReportCoverage returns the artifact's coverage summary. Optional.
	ReportCoverage(ctx context.Context, scope SnapshotScope) (*CoverageReport, error)

	// CleanupArtifact removes one artifact this system created. Providers
	// that cannot verify ownership must refuse (return ErrUnsupported)
	// rather than delete a shared external index.
	CleanupArtifact(ctx context.Context, handle string) error
}

// DefaultQueryTimeout bounds individual provider calls (CLI run / MCP tool
// call). Task-level timeouts are larger and enforced by the caller.
const DefaultQueryTimeout = 2 * time.Minute
