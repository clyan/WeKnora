package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Code Wiki domain model.
//
// A code_wiki knowledge base maps one project onto multiple Git
// repositories. Analysis inputs are immutable snapshots (one repository at
// one commit with one filter configuration). Analysis itself is delegated
// to a pluggable CodeAnalysisProvider — the builtin codebase-memory CLI or
// a user-configured MCP service — described by a per-KB analysis profile
// whose config version freezes into every run.
//
//	KB (type=code_wiki)
//	 ├── code_wiki_analysis_profiles  (KB-level provider config: kind, MCP service, tool mappings, capability check)
//	 └── code_wiki_repositories       (binding: GitLab source + project + ref + filters)
//	     └── code_wiki_snapshots      (immutable source: commit SHA + filtered tree on disk)
//	         └── code_wiki_analysis_artifacts (provider-specific index/analysis products)
//	 └── code_wiki_runs               (one generation batch; freezes profile version + manifest)
//	     ├── code_wiki_run_repositories (frozen repo→snapshot→artifact manifest)
//	     └── code_wiki_run_pages       (staged pages awaiting publish)
//	         └── code_wiki_page_evidence (per-page-version code citations)

// KnowledgeBaseTypeCodeWiki represents the code wiki knowledge base type:
// one KB = one project backed by multiple related Git repositories.
const KnowledgeBaseTypeCodeWiki = "code_wiki"

// IsValidKnowledgeBaseType reports whether the given KB type is known.
// Unknown types would silently disappear from type-filtered UIs and
// capability checks, so write paths reject them.
func IsValidKnowledgeBaseType(t string) bool {
	switch t {
	case KnowledgeBaseTypeDocument, KnowledgeBaseTypeFAQ, KnowledgeBaseTypeWiki, KnowledgeBaseTypeCodeWiki:
		return true
	default:
		return false
	}
}

// IsCodeWiki reports whether this KB is a code wiki knowledge base.
func (kb *KnowledgeBase) IsCodeWiki() bool {
	return kb != nil && kb.Type == KnowledgeBaseTypeCodeWiki
}

// Repository binding roles. The role feeds the default document outline
// (frontend / backend / shared-service viewpoints) and is display-only
// metadata for the generation prompts.
const (
	CodeWikiRepoRoleFrontend = "frontend"
	CodeWikiRepoRoleBackend  = "backend"
	CodeWikiRepoRoleService  = "service" // shared/internal microservice
	CodeWikiRepoRoleLibrary  = "library" // shared package / module
	CodeWikiRepoRoleOther    = "other"
)

// IsValidCodeWikiRepoRole reports whether role is one of the known roles.
func IsValidCodeWikiRepoRole(role string) bool {
	switch role {
	case CodeWikiRepoRoleFrontend, CodeWikiRepoRoleBackend, CodeWikiRepoRoleService,
		CodeWikiRepoRoleLibrary, CodeWikiRepoRoleOther:
		return true
	default:
		return false
	}
}

// Repository binding status values. The binding status summarizes the latest
// sync attempt; the authoritative per-input state is the snapshot row.
const (
	CodeWikiRepoStatusIdle    = "idle"    // never synced
	CodeWikiRepoStatusSyncing = "syncing" // a sync task is in flight
	CodeWikiRepoStatusReady   = "ready"   // latest snapshot prepared
	CodeWikiRepoStatusError   = "error"   // last sync failed (see error_message)
)

// CodeWikiConfig stores code-wiki-specific configuration for a knowledge
// base. Only meaningful when KB type is code_wiki. The generation model and
// custom instructions are intentionally NOT duplicated here — they reuse
// WikiConfig (SynthesisModelID / ContentInstructions / ExtractionInstructions)
// so operators maintain one set of generation settings per KB. Analysis
// provider selection lives in code_wiki_analysis_profiles; the version
// reference below is a denormalized display/freeze pointer only.
type CodeWikiConfig struct {
	// Languages hints the analysis and generation prompts (e.g. ["zh"], ["en"]).
	// Empty = follow request language.
	Languages []string `yaml:"languages,omitempty" json:"languages,omitempty"`
	// MaxPagesPerRun caps how many pages one generation run may produce.
	// 0 falls back to DefaultCodeWikiMaxPagesPerRun.
	MaxPagesPerRun int `yaml:"max_pages_per_run,omitempty" json:"max_pages_per_run,omitempty"`
	// MaxModelCallsPerRun caps total LLM calls per run (budget guard).
	// 0 falls back to DefaultCodeWikiMaxModelCalls.
	MaxModelCallsPerRun int `yaml:"max_model_calls_per_run,omitempty" json:"max_model_calls_per_run,omitempty"`
	// RunTimeoutMinutes bounds one generation run. 0 falls back to
	// DefaultCodeWikiRunTimeoutMinutes.
	RunTimeoutMinutes int `yaml:"run_timeout_minutes,omitempty" json:"run_timeout_minutes,omitempty"`
	// AnalysisProfileVersion mirrors the current analysis profile's
	// config_version for quick display. The profile table is the source of
	// truth; runs freeze the version they executed with.
	AnalysisProfileVersion int `yaml:"analysis_profile_version,omitempty" json:"analysis_profile_version,omitempty"`
}

// Budget/limits defaults for code wiki generation.
const (
	DefaultCodeWikiMaxPagesPerRun    = 60
	DefaultCodeWikiMaxModelCalls     = 200
	DefaultCodeWikiRunTimeoutMinutes = 120
	MaxCodeWikiPagesPerRunHardCap    = 200
	MaxCodeWikiModelCallsHardCap     = 800

	MaxCodeWikiRunTimeoutMinutesCap = 480
	MaxCodeWikiRepositoriesPerKB    = 30
	MaxCodeWikiIncludePathsPerRepo  = 50
	// MaxCodeWikiExcludePatternsPerRepo bounds user-supplied exclude globs.
	MaxCodeWikiExcludePatternsPerRepo = 100
	// MaxCodeWikiToolMappings bounds the per-profile mapping entries.
	MaxCodeWikiToolMappings = 24
)

// PagesCapOrDefault returns the effective per-run page cap.
func (c *CodeWikiConfig) PagesCapOrDefault() int {
	if c == nil || c.MaxPagesPerRun <= 0 {
		return DefaultCodeWikiMaxPagesPerRun
	}
	return c.MaxPagesPerRun
}

// ModelCallsCapOrDefault returns the effective per-run model call cap.
func (c *CodeWikiConfig) ModelCallsCapOrDefault() int {
	if c == nil || c.MaxModelCallsPerRun <= 0 {
		return DefaultCodeWikiMaxModelCalls
	}
	return c.MaxModelCallsPerRun
}

// TimeoutOrDefault returns the effective run timeout.
func (c *CodeWikiConfig) TimeoutOrDefault() time.Duration {
	minutes := DefaultCodeWikiRunTimeoutMinutes
	if c != nil && c.RunTimeoutMinutes > 0 {
		minutes = c.RunTimeoutMinutes
	}
	if minutes > MaxCodeWikiRunTimeoutMinutesCap {
		minutes = MaxCodeWikiRunTimeoutMinutesCap
	}
	return time.Duration(minutes) * time.Minute
}

// Normalize clamps configured budgets into their allowed ranges.
func (c *CodeWikiConfig) Normalize() {
	if c == nil {
		return
	}
	if c.MaxPagesPerRun > MaxCodeWikiPagesPerRunHardCap {
		c.MaxPagesPerRun = MaxCodeWikiPagesPerRunHardCap
	}
	if c.MaxModelCallsPerRun > MaxCodeWikiModelCallsHardCap {
		c.MaxModelCallsPerRun = MaxCodeWikiModelCallsHardCap
	}
	if c.RunTimeoutMinutes > MaxCodeWikiRunTimeoutMinutesCap {
		c.RunTimeoutMinutes = MaxCodeWikiRunTimeoutMinutesCap
	}
}

// Value implements the driver.Valuer interface.
func (c CodeWikiConfig) Value() (driver.Value, error) { return json.Marshal(c) }

// Scan implements the sql.Scanner interface.
func (c *CodeWikiConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		if s, ok := value.(string); ok {
			b = []byte(s)
		} else {
			return nil
		}
	}
	return json.Unmarshal(b, c)
}

// --- Analysis provider model ------------------------------------------------

// Code wiki analysis provider kinds.
const (
	// CodeWikiProviderBuiltin is the default adapter driving the pinned
	// codebase-memory-mcp executable through a controlled CLI/JSON surface.
	CodeWikiProviderBuiltin = "builtin_codebase"
	// CodeWikiProviderCustomMCP reuses a tenant-configured MCP service
	// (SSE / HTTP Streamable) with declarative capability→tool mappings.
	CodeWikiProviderCustomMCP = "custom_mcp"
)

// IsValidCodeWikiProviderKind reports whether kind is a known provider.
func IsValidCodeWikiProviderKind(kind string) bool {
	switch kind {
	case CodeWikiProviderBuiltin, CodeWikiProviderCustomMCP:
		return true
	default:
		return false
	}
}

// Analysis capability identifiers. These are the stable capability keys
// used by the provider interface, tool mappings and capability reports.
const (
	// CodeWikiCapSnapshotScope: queries can be pinned to an exact
	// repository+commit+filter scope (REQUIRED for publishable docs).
	CodeWikiCapSnapshotScope = "snapshot_scope"
	// CodeWikiCapCodeSearch: bounded, snapshot-scoped code search (REQUIRED).
	CodeWikiCapCodeSearch = "code_search"
	// CodeWikiCapSourceRead: precise file+line source reading (REQUIRED;
	// satisfiable by provider tools or WeKnora's local snapshot).
	CodeWikiCapSourceRead = "source_read"
	// CodeWikiCapSnapshotPrepare: create/verify a snapshot index (REQUIRED,
	// either prepare or external binding).
	CodeWikiCapSnapshotPrepare = "snapshot_prepare"
	// CodeWikiCapDirectory: directory/file tree structure (optional).
	CodeWikiCapDirectory = "directory"
	// CodeWikiCapSymbols: symbol definitions/references (optional).
	CodeWikiCapSymbols = "symbols"
	// CodeWikiCapCallGraph: call/dependency relations (optional).
	CodeWikiCapCallGraph = "call_graph"
	// CodeWikiCapCrossRepo: native cross-repository analysis (optional).
	CodeWikiCapCrossRepo = "cross_repo"
	// CodeWikiCapCoverage: coverage reporting + artifact cleanup (optional).
	CodeWikiCapCoverage = "coverage"
)

// RequiredCodeWikiCapabilities lists capabilities the first version cannot
// generate publishable documents without. Missing optional capabilities
// degrade the analysis (documented in the run) rather than block it.
var RequiredCodeWikiCapabilities = []string{
	CodeWikiCapSnapshotScope, CodeWikiCapCodeSearch, CodeWikiCapSourceRead,
}

// CodeWikiCapabilityStatus is the per-capability outcome of a check.
type CodeWikiCapabilityStatus struct {
	Capability string `json:"capability"`
	// Available reports whether the capability is usable.
	Available bool `json:"available"`
	// Source tells how it is satisfied: "provider" | "builtin" | "local" | ""
	// (local = WeKnora reads its own snapshot, e.g. source_read fallback).
	Source string `json:"source,omitempty"`
	// Tool names the mapped provider tool (when Source=provider).
	Tool string `json:"tool,omitempty"`
	// Detail explains failures/degradations in user-facing terms.
	Detail string `json:"detail,omitempty"`
}

// CodeWikiCapabilityReport is the layered capability-check result stored on
// the analysis profile (and frozen into runs). It deliberately separates
// "connected" from "mappable" from "snapshot-ready" from "can generate".
type CodeWikiCapabilityReport struct {
	// CheckedAt is when the check ran.
	CheckedAt time.Time `json:"checked_at"`
	// Connected reports raw service/CLI reachability.
	Connected bool `json:"connected"`
	// ConnectDetail explains a connection failure.
	ConnectDetail string `json:"connect_detail,omitempty"`
	// Capabilities carries per-capability outcomes.
	Capabilities []CodeWikiCapabilityStatus `json:"capabilities"`
	// Blocking lists capability keys whose absence blocks generation.
	Blocking []string `json:"blocking,omitempty"`
	// Notes records degradations (e.g. "no call graph: relations are
	// source-verified only").
	Notes []string `json:"notes,omitempty"`
	// MeetsMinimum is true when no required capability is missing.
	MeetsMinimum bool `json:"meets_minimum"`
}

// Capability returns the status entry for one capability key.
func (r *CodeWikiCapabilityReport) Capability(key string) *CodeWikiCapabilityStatus {
	if r == nil {
		return nil
	}
	for i := range r.Capabilities {
		if r.Capabilities[i].Capability == key {
			return &r.Capabilities[i]
		}
	}
	return nil
}

// HasCapability reports whether a capability is available per the report.
func (r *CodeWikiCapabilityReport) HasCapability(key string) bool {
	c := r.Capability(key)
	return c != nil && c.Available
}

// CodeWikiToolMapping is one declarative capability mapping for the
// custom_mcp provider: which tool implements a capability, how request
// fields are bound, and where results live in the response. Values are
// restricted to static strings, controlled context variables ({{repo}},
// {{commit}}, {{path}}, {{query}}, {{symbol}}, {{offset}}, {{limit}}) and
// dotted field paths — never executable code.
type CodeWikiToolMapping struct {
	// Capability is one of the CodeWikiCap* keys.
	Capability string `json:"capability"`
	// Tool is the MCP tool name.
	Tool string `json:"tool"`
	// Input maps provider-neutral argument names to either a literal string
	// or a context variable reference (e.g. {"query": "{{query}}"}).
	Input map[string]string `json:"input,omitempty"`
	// Output selects result fields from the tool response:
	//   results: dotted path to the array of hits
	//   path/line_start/line_end/symbol/snippet/content: per-hit field paths
	Output CodeWikiMappingOutput `json:"output,omitempty"`
}

// CodeWikiMappingOutput selects where standardized result fields live in a
// tool response. Every value is a dotted JSON path (empty = not provided).
type CodeWikiMappingOutput struct {
	// ResultsPath points at the array of hits ("hits.items").
	ResultsPath string `json:"results_path,omitempty"`
	PathField      string `json:"path,omitempty"`
	StartLineField string `json:"start_line,omitempty"`
	EndLineField   string `json:"end_line,omitempty"`
	SymbolField    string `json:"symbol,omitempty"`
	SnippetField   string `json:"snippet,omitempty"`
	ContentField   string `json:"content,omitempty"`
	// TextField points at the whole-result text when the tool returns a
	// text blob instead of structured items; WeKnora then extracts
	// file:line references heuristically. Structured paths take priority.
	TextField string `json:"text,omitempty"`
	// IsErrorField marks a boolean error flag in the response.
	IsErrorField string `json:"is_error,omitempty"`
}

// Snapshot delivery methods for remote (custom_mcp) providers: how the
// service obtains the code it analyzes. Empty defaults to local (builtin).
const (
	// CodeWikiDeliveryLocal: builtin provider reads WeKnora's own snapshot
	// directory (custom_mcp cannot use this).
	CodeWikiDeliveryLocal = "local"
	// CodeWikiDeliverySharedDir: admin-configured shared read-only snapshot
	// root mounted by both WeKnora and the service, with controlled path
	// mapping rooted at that directory.
	CodeWikiDeliverySharedDir = "shared_dir"
	// CodeWikiDeliveryImport: the service exposes a snapshot-import tool;
	// WeKnora delivers the filtered snapshot through restricted upload.
	CodeWikiDeliveryImport = "import"
	// CodeWikiDeliveryExternalIndex: bind an externally prepared index and
	// verify its repository/commit/filter scope before use.
	CodeWikiDeliveryExternalIndex = "external_index"
)

// IsValidCodeWikiDelivery reports whether delivery is a known method.
func IsValidCodeWikiDelivery(delivery string) bool {
	switch delivery {
	case CodeWikiDeliveryLocal, CodeWikiDeliverySharedDir, CodeWikiDeliveryImport, CodeWikiDeliveryExternalIndex:
		return true
	default:
		return false
	}
}

// CodeWikiAnalysisProfile is the KB-level analysis provider configuration.
// One row per KB (unique on knowledge_base_id); every save increments
// ConfigVersion so runs and artifacts freeze the version they used.
// Credentials and connection settings are NOT stored here — custom MCP
// profiles reference a tenant MCP service that owns auth, timeouts and
// tool enable/approval policy.
type CodeWikiAnalysisProfile struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);uniqueIndex"`
	// Provider kind: builtin_codebase | custom_mcp
	ProviderKind string `json:"provider_kind" gorm:"type:varchar(32);default:'builtin_codebase'"`
	// MCPServiceID references the tenant MCP service (custom_mcp only).
	MCPServiceID string `json:"mcp_service_id,omitempty" gorm:"column:mcp_service_id;type:varchar(36)"`
	// ToolMappings: declarative capability→tool bindings (custom_mcp only).
	ToolMappings JSON `json:"tool_mappings,omitempty" gorm:"type:jsonb"`
	// SnapshotDelivery: local | shared_dir | import | external_index.
	SnapshotDelivery string `json:"snapshot_delivery" gorm:"type:varchar(32);default:'local'"`
	// SharedRoot is the admin-configured shared snapshot root (shared_dir
	// delivery); mappings are confined under this root.
	SharedRoot string `json:"shared_root,omitempty" gorm:"type:varchar(1024)"`
	// ExternalIndexRef holds the externally prepared index handle(s)
	// (external_index delivery), with repository/commit scope for verify.
	ExternalIndexRef JSON `json:"external_index_ref,omitempty" gorm:"type:jsonb"`
	// CapabilityCheck is the latest capability report.
	CapabilityCheck JSON `json:"capability_check,omitempty" gorm:"type:jsonb"`
	// MeetsMinimum mirrors CapabilityCheck.MeetsMinimum for cheap filtering.
	MeetsMinimum bool `json:"meets_minimum" gorm:"default:false"`
	// ConfigVersion increments on every provider/mapping change.
	ConfigVersion int `json:"config_version" gorm:"default:1"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName specifies the database table name.
func (CodeWikiAnalysisProfile) TableName() string { return "code_wiki_analysis_profiles" }

// ParsedToolMappings decodes the stored tool mapping JSON.
func (p *CodeWikiAnalysisProfile) ParsedToolMappings() ([]CodeWikiToolMapping, error) {
	if p == nil || len(p.ToolMappings) == 0 {
		return nil, nil
	}
	var out []CodeWikiToolMapping
	if err := json.Unmarshal(p.ToolMappings, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ParsedCapabilityCheck decodes the stored capability report.
func (p *CodeWikiAnalysisProfile) ParsedCapabilityCheck() (*CodeWikiCapabilityReport, error) {
	if p == nil || len(p.CapabilityCheck) == 0 {
		return nil, nil
	}
	var out CodeWikiCapabilityReport
	if err := json.Unmarshal(p.CapabilityCheck, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CodeWikiRepository is one repository binding: a (GitLab data source,
// project, ref) triple analyzed as part of the code wiki. The binding is
// the single source of truth for which projects participate — code-mode
// GitLab data sources do not keep a parallel settings.projects list.
// Renaming the display name does not change the ID; changing ref or
// filters requires a new snapshot (snapshots are immutable).
type CodeWikiRepository struct {
	// Unique identifier (UUID)
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// Workspace ID for multi-workspace isolation
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// Owning knowledge base (must be a code_wiki KB)
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index:idx_code_wiki_repos_kb"`
	// GitLab data source providing credentials/network config for this project
	SourceID string `json:"source_id" gorm:"type:varchar(36);index:idx_code_wiki_repos_kb"`
	// GitLab project ID (numeric id or group/project path)
	ProjectID string `json:"project_id" gorm:"type:varchar(512)"`
	// Display name (defaults to path_with_namespace at bind time)
	DisplayName string `json:"display_name" gorm:"type:varchar(512)"`
	// Repository web URL captured at bind time (for evidence links)
	WebURL string `json:"web_url" gorm:"type:varchar(1024)"`
	// Role in the system: frontend | backend | service | library | other
	Role string `json:"role" gorm:"type:varchar(32)"`
	// Branch/tag to track; empty = project default branch
	Ref string `json:"ref" gorm:"type:varchar(255)"`
	// Optional directory roots to include (repo-relative, forward slashes)
	IncludePaths StringArray `json:"include_paths" gorm:"type:json"`
	// Glob patterns for paths to exclude (matched against repo-relative path)
	ExcludePatterns StringArray `json:"exclude_patterns" gorm:"type:json"`
	// Whether this binding participates in sync/generation
	Enabled bool `json:"enabled" gorm:"default:true"`
	// Sync status summary (idle | syncing | ready | error)
	Status string `json:"status" gorm:"type:varchar(32);default:'idle'"`
	// Latest successful snapshot for quick display
	LastSnapshotID *string `json:"last_snapshot_id,omitempty" gorm:"type:varchar(36)"`
	// Commit SHA of the latest successful snapshot
	LastCommitSHA string `json:"last_commit_sha,omitempty" gorm:"type:varchar(64)"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	// Last sync error message (empty when status != error)
	ErrorMessage string `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName specifies the database table name.
func (CodeWikiRepository) TableName() string { return "code_wiki_repositories" }

// CodeWikiSnapshot is one immutable SOURCE input: the repository tree at a
// fixed commit SHA, filtered by the binding's include/exclude rules and
// materialized on local disk. Provider-specific indexes live in
// code_wiki_analysis_artifacts — the same snapshot can be analyzed by
// different providers without touching each other's products.
type CodeWikiSnapshot struct {
	// Unique identifier (UUID)
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// Owning repository binding
	RepositoryID string `json:"repository_id" gorm:"type:varchar(36);index"`
	// Resolved commit SHA this snapshot froze (content is fetched by SHA,
	// never by moving ref, so a branch advancing mid-sync cannot mix versions)
	CommitSHA string `json:"commit_sha" gorm:"type:varchar(64);index"`
	// Hash of the filter configuration (include paths + exclude patterns),
	// used to reuse snapshots across syncs with unchanged inputs
	FilterHash string `json:"filter_hash" gorm:"type:varchar(64)"`
	// Local directory holding the filtered tree (under the code-wiki data root)
	LocalPath string `json:"local_path" gorm:"type:varchar(1024)"`
	// Number of files retained after filtering
	FileCount int `json:"file_count"`
	// Total bytes of retained files
	TotalSizeBytes int64 `json:"total_size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName specifies the database table name.
func (CodeWikiSnapshot) TableName() string { return "code_wiki_snapshots" }

// Analysis artifact status values.
const (
	CodeWikiArtifactStatusPending   = "pending"   // snapshot ready, artifact not prepared
	CodeWikiArtifactStatusPreparing = "preparing" // provider is preparing/verifying
	CodeWikiArtifactStatusReady     = "ready"     // provider verified, queryable
	CodeWikiArtifactStatusFailed    = "failed"    // prepare/verify failed
)

// CodeWikiAnalysisArtifact is the provider-specific product of analyzing
// one snapshot under one analysis-profile version: an index handle, graph
// project or external binding, plus coverage metadata. Switching providers
// or mappings creates new artifacts; old artifacts (and the documents that
// cite them) keep resolving.
type CodeWikiAnalysisArtifact struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// Repository binding and analyzed snapshot
	RepositoryID string `json:"repository_id" gorm:"type:varchar(36);index"`
	SnapshotID string `json:"snapshot_id" gorm:"type:varchar(36);index"`
	// Profile config version this artifact was prepared under
	ProfileVersion int `json:"profile_version"`
	// Engine version reported by the provider (CLI version, service version)
	EngineVersion string `json:"engine_version,omitempty" gorm:"type:varchar(32)"`
	// ProviderHandle is the opaque external handle (graph project name,
	// index id, …) used when querying; never a raw local path.
	ProviderHandle string `json:"provider_handle,omitempty" gorm:"type:varchar(255)"`
	// CoverageSummary: indexed languages, unparsed files, …
	CoverageSummary JSON `json:"coverage_summary,omitempty" gorm:"type:jsonb"`
	// Status: pending | preparing | ready | failed
	Status string `json:"status" gorm:"type:varchar(32);default:'pending'"`
	// Prepare/verify error message
	ErrorMessage string `json:"error_message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName specifies the database table name.
func (CodeWikiAnalysisArtifact) TableName() string { return "code_wiki_analysis_artifacts" }

// Code Wiki run status values. A run moves pending → (phases) → staged →
// published; the previously published run transitions to superseded when a
// new run publishes. failed/canceled are terminal. paused is a resting state:
// the plan and completed pages persist, resume continues the remaining pages.
const (
	CodeWikiRunStatusPending    = "pending"
	CodeWikiRunStatusPreparing  = "preparing"  // freezing snapshots / preparing artifacts
	CodeWikiRunStatusAnalyzing  = "analyzing"  // cross-repository analysis
	CodeWikiRunStatusPlanning   = "planning"   // outline planning (LLM)
	CodeWikiRunStatusGenerating = "generating" // per-page evidence + generation
	CodeWikiRunStatusValidating = "validating" // evidence reference validation
	CodeWikiRunStatusStaged     = "staged"     // candidate pages await publish
	CodeWikiRunStatusPaused     = "paused"     // user-paused; resume continues
	CodeWikiRunStatusPublishing = "publishing"
	CodeWikiRunStatusPublished  = "published"
	CodeWikiRunStatusSuperseded = "superseded"
	CodeWikiRunStatusFailed     = "failed"
	CodeWikiRunStatusCanceled   = "canceled"
)

// IsTerminalCodeWikiRunStatus reports whether no further transitions occur.
func IsTerminalCodeWikiRunStatus(status string) bool {
	switch status {
	case CodeWikiRunStatusPublished, CodeWikiRunStatusSuperseded,
		CodeWikiRunStatusFailed, CodeWikiRunStatusCanceled:
		return true
	default:
		return false
	}
}

// CodeWikiRun is one generation batch. The run freezes the analysis profile
// version (with its capability report), the repository/snapshot/artifact
// manifest, and generation inputs; ends either staged (awaiting explicit
// publish) or failed/canceled. Exactly one run per KB may be
// status=published (partial unique index).
type CodeWikiRun struct {
	// NoChanges is response-only: identical published inputs need no new run.
	NoChanges bool `json:"no_changes,omitempty" gorm:"-"`
	// Unique identifier (UUID)
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// Run status (see CodeWikiRunStatus*)
	Status string `json:"status" gorm:"type:varchar(32);default:'pending';index"`
	// Human-readable current phase label for progress display
	Phase string `json:"phase,omitempty" gorm:"type:varchar(255)"`
	// Model used for this run (frozen at start from WikiConfig/SummaryModelID)
	ModelID string `json:"model_id,omitempty" gorm:"type:varchar(128)"`
	// Analysis profile config version this run executes under
	ProfileVersion int `json:"profile_version"`
	// Capabilities is the frozen capability report at run start; generation
	// adapts to it and the published docs disclose the limits.
	Capabilities JSON `json:"capabilities,omitempty" gorm:"type:jsonb"`
	// Frozen generation inputs: languages, content/extraction instructions,
	// budget caps, default outline — captured so later config edits cannot
	// silently change what an in-flight or historical run produced.
	ConfigSnapshot JSON `json:"config_snapshot,omitempty" gorm:"type:jsonb"`
	// Progress counters (stage-based, not fake percentages)
	TotalPages int `json:"total_pages"`
	CompletedPages int `json:"completed_pages"`
	FailedPages int `json:"failed_pages"`
	// Actual budget consumption
	ModelCalls int `json:"model_calls"`
	TokensUsed int64 `json:"tokens_used"`
	// Error message when status=failed
	ErrorMessage string `json:"error_message,omitempty"`
	// CancelRequested lets users flag cancellation; the worker observes it
	// between stages and transitions the run to canceled.
	CancelRequested bool `json:"cancel_requested" gorm:"default:false"`
	// PauseRequested lets users pause between pages; the worker observes it
	// between stages and transitions the run to paused (resumable).
	PauseRequested bool `json:"pause_requested" gorm:"default:false"`
	// IdempotencyKey deduplicates concurrent submissions
	IdempotencyKey string `json:"idempotency_key,omitempty" gorm:"type:varchar(128);index"`
	// LeaseOwner identifies the worker process holding this run;
	// LeaseExpiresAt bounds recovery after a crashed worker.
	LeaseOwner string `json:"lease_owner,omitempty" gorm:"type:varchar(128)"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName specifies the database table name.
func (CodeWikiRun) TableName() string { return "code_wiki_runs" }

// CodeWikiRunRepository freezes one repository's participation in a run:
// which snapshot and which analysis artifact are used, and — when an
// enabled repository was excluded — why. During generation the run never
// follows the branch forward; the snapshot is the analysis input.
type CodeWikiRunRepository struct {
	Ref string `json:"ref,omitempty" gorm:"-"`
	FilterHash string `json:"filter_hash,omitempty" gorm:"-"`
	IncludePaths StringArray `json:"include_paths,omitempty" gorm:"-"`
	ExcludePatterns StringArray `json:"exclude_patterns,omitempty" gorm:"-"`
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	RunID string `json:"run_id" gorm:"type:varchar(36);index"`
	RepositoryID string `json:"repository_id" gorm:"type:varchar(36)"`
	SnapshotID string `json:"snapshot_id,omitempty" gorm:"type:varchar(36)"`
	// ArtifactID references the analysis product used for this repo in this
	// run (empty when the repository was excluded).
	ArtifactID string `json:"artifact_id,omitempty" gorm:"type:varchar(36)"`
	Role string `json:"role" gorm:"type:varchar(32)"`
	DisplayName string `json:"display_name" gorm:"type:varchar(512)"`
	CommitSHA string `json:"commit_sha,omitempty" gorm:"type:varchar(64)"`
	// Excluded records an enabled repository that could not join this run
	// (e.g. artifact not ready); ExcludeReason is user-facing. The
	// generation result must surface the actual scope, never silently omit.
	Excluded bool `json:"excluded" gorm:"default:false"`
	ExcludeReason string `json:"exclude_reason,omitempty" gorm:"type:varchar(512)"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName specifies the database table name.
func (CodeWikiRunRepository) TableName() string { return "code_wiki_run_repositories" }

// Run page staging status values.
const (
	CodeWikiRunPageStatusPending   = "pending"  // planned, not yet generated (persisted plan)
	CodeWikiRunPageStatusStaged    = "staged"
	CodeWikiRunPageStatusConflict  = "conflict" // live page edited since baseline
	CodeWikiRunPageStatusPublished = "published"
	CodeWikiRunPageStatusSkipped   = "skipped" // dropped by user choice at publish
	CodeWikiRunPageStatusPreserved = "preserved" // unchanged page, no live-row/evidence write
)

// CodeWikiRunPage is one candidate page produced by a run, staged until the
// run is published. PageKey is the stable identity (wiki slug) so repeated
// runs update the same page instead of accumulating duplicates.
// BaselineVersion records the live page version at generation time; publish
// verifies the live page has not been human-edited since (otherwise the
// page is marked conflict and the batch publish pauses).
type CodeWikiRunPage struct {
	PlanMetadata JSON `json:"plan_metadata,omitempty" gorm:"type:jsonb"`
	SortOrder int `json:"sort_order" gorm:"default:0"`
	// Response-only comparison, exposed only while the raw baseline matches.
	BaselineContent *string `json:"baseline_content,omitempty" gorm:"-"`
	BaselineChanged bool `json:"baseline_changed,omitempty" gorm:"-"`
	SectionChanges []CodeWikiSectionChange `json:"section_changes,omitempty" gorm:"-"`
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	RunID string `json:"run_id" gorm:"type:varchar(36);index"`
	// Stable page identity: the wiki slug the page publishes under
	PageKey string `json:"page_key" gorm:"type:varchar(255)"`
	Title string `json:"title" gorm:"type:varchar(512)"`
	// Existing wiki page type (index | synthesis | summary | concept | …);
	// the code-wiki document category rides in Category, not in page_type,
	// so existing type whitelists and filters keep working.
	PageType string `json:"page_type" gorm:"type:varchar(32)"`
	// Code wiki document category (see CodeWikiPageCategory*)
	Category string `json:"category" gorm:"type:varchar(32)"`
	// Directory breadcrumb for the wiki browser (category path)
	CategoryPath StringArray `json:"category_path" gorm:"type:json"`
	Content string `json:"content" gorm:"type:text"`
	Summary string `json:"summary" gorm:"type:text"`
	// Evidence citations referenced by this page, as a JSON array of
	// CodeWikiEvidenceSpec (IDs assigned before generation; validated before
	// staging). Kept inline because run pages are immutable once staged.
	Evidence JSON `json:"evidence,omitempty" gorm:"type:jsonb"`
	// Baseline of the live wiki page at generation time. 0 = page is new.
	BaselineVersion int `json:"baseline_version"`
	// BaselineLastEditSource records who authored the baseline version
	// (pipeline vs user) so publish can protect human edits.
	BaselineLastEditSource string `json:"baseline_last_edit_source,omitempty" gorm:"type:varchar(16)"`
	// Pending page progress: pending | retrieving | generating | validating.
	// Once staged, evidence validation result: passed | failed.
	ValidationStatus string `json:"validation_status" gorm:"type:varchar(32);default:'pending'"`
	// Structured validation errors (missing files, out-of-range lines, …)
	ValidationErrors JSON `json:"validation_errors,omitempty" gorm:"type:jsonb"`
	// Staging status: pending | staged | conflict | published | skipped
	Status string `json:"status" gorm:"type:varchar(32);default:'staged'"`
	// GenerationError records per-page failure (page omitted from publish)
	GenerationError string `json:"generation_error,omitempty"`
	// Scope is the planned page scope (analysis focus, persisted so a paused
	// run can resume the remaining pages without re-planning).
	Scope string `json:"scope,omitempty" gorm:"type:varchar(1024)"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName specifies the database table name.
func (CodeWikiRunPage) TableName() string { return "code_wiki_run_pages" }

// Code wiki page categories. These drive the default document outline and
// page classification; they are metadata, not wiki page types.
const (
	CodeWikiPageCategoryOverview     = "overview"     // 项目概览
	CodeWikiPageCategoryArchitecture = "architecture" // 系统架构
	CodeWikiPageCategoryInteraction  = "interaction"  // 前后端交互
	CodeWikiPageCategoryWorkflow     = "workflow"     // 业务流程
	CodeWikiPageCategoryRepository   = "repository"   // 逐仓库说明
	CodeWikiPageCategoryDevOps       = "devops"       // 开发运维
	CodeWikiPageCategoryAnalysis     = "analysis"     // 分析说明/覆盖范围
)

// IsValidCodeWikiPageCategory reports whether category is known.
func IsValidCodeWikiPageCategory(category string) bool {
	switch category {
	case CodeWikiPageCategoryOverview, CodeWikiPageCategoryArchitecture,
		CodeWikiPageCategoryInteraction, CodeWikiPageCategoryWorkflow,
		CodeWikiPageCategoryRepository, CodeWikiPageCategoryDevOps,
		CodeWikiPageCategoryAnalysis:
		return true
	default:
		return false
	}
}

// Evidence types describe what a citation points at.
const (
	CodeWikiEvidenceTypeFile   = "file"   // whole-file context
	CodeWikiEvidenceTypeSymbol = "symbol" // function/type definition site
	CodeWikiEvidenceTypeCall   = "call"   // call/reference site
	CodeWikiEvidenceTypeConfig = "config" // build/deploy/route config
)

// CodeWikiEvidenceSpec is the citation contract handed to the model BEFORE
// generation: the model may only reference evidence by ID, and every
// reference must survive post-generation validation (file exists in the
// snapshot, lines in range, quote matches). Serialized inside
// CodeWikiRunPage.Evidence and expanded into CodeWikiPageEvidence rows at
// publish time, bound to the published page version.
type CodeWikiEvidenceSpec struct {
	// ID the model cites, e.g. "E1"
	ID string `json:"id"`
	RepositoryID string `json:"repository_id"`
	SnapshotID string `json:"snapshot_id"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Path string `json:"path"`
	StartLine int `json:"start_line,omitempty"`
	EndLine int `json:"end_line,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Type string `json:"type,omitempty"`
	// Quote is a short excerpt (bounded) used to verify the reference still
	// resolves to the cited content at validation time.
	Quote string `json:"quote,omitempty"`
}

// CodeWikiPageEvidence is a published citation bound to one page version.
// Historical versions keep their own rows, so reverting a page restores the
// exact references that version was generated from.
type CodeWikiPageEvidence struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// Published wiki page
	PageID string `json:"page_id" gorm:"type:varchar(36);index:idx_code_wiki_evidence_page_version"`
	// Version of the page this evidence belongs to
	PageVersion int `json:"page_version" gorm:"index:idx_code_wiki_evidence_page_version"`
	// Originating run
	RunID string `json:"run_id" gorm:"type:varchar(36);index"`
	// Evidence ID as cited in the content (E1, E2, …)
	EvidenceKey string `json:"evidence_key" gorm:"type:varchar(16)"`
	RepositoryID string `json:"repository_id" gorm:"type:varchar(36)"`
	SnapshotID string `json:"snapshot_id" gorm:"type:varchar(36)"`
	CommitSHA string `json:"commit_sha" gorm:"type:varchar(64)"`
	Path string `json:"path" gorm:"type:varchar(1024)"`
	StartLine int `json:"start_line"`
	EndLine int `json:"end_line"`
	Symbol string `json:"symbol,omitempty" gorm:"type:varchar(512)"`
	Type string `json:"type,omitempty" gorm:"type:varchar(32)"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName specifies the database table name.
func (CodeWikiPageEvidence) TableName() string { return "code_wiki_page_evidence" }

// CodeWikiPageEvidenceView is the API response shape for one evidence row:
// the citation plus resolved display fields (repository name, GitLab URL at
// the frozen commit) and the cited excerpt read from the local snapshot.
type CodeWikiPageEvidenceView struct {
	CodeWikiPageEvidence
	RepositoryName string `json:"repository_name,omitempty"`
	RepositoryRole string `json:"repository_role,omitempty"`
	// WebURL points at the file at the frozen commit on GitLab
	WebURL string `json:"web_url,omitempty"`
	// Lines is the cited excerpt from the snapshot (bounded), for local
	// display without a GitLab round-trip
	Lines string `json:"lines,omitempty"`
}

// --- API request/response payloads -----------------------------------------

// CodeWikiAnalysisProfileRequest updates the KB's analysis provider config.
type CodeWikiAnalysisProfileRequest struct {
	ProviderKind string `json:"provider_kind" binding:"required"`
	// MCPServiceID: the tenant MCP service to use (custom_mcp only).
	MCPServiceID string `json:"mcp_service_id,omitempty"`
	ToolMappings []CodeWikiToolMapping `json:"tool_mappings,omitempty"`
	SnapshotDelivery string `json:"snapshot_delivery,omitempty"`
	SharedRoot string `json:"shared_root,omitempty"`
}

// CodeWikiRepositoryUpsertRequest creates or updates a repository binding.
type CodeWikiRepositoryUpsertRequest struct {
	SourceID string `json:"source_id" binding:"required"`
	ProjectID string `json:"project_id" binding:"required"`
	DisplayName string `json:"display_name,omitempty"`
	Role string `json:"role,omitempty"`
	Ref string `json:"ref,omitempty"`
	IncludePaths []string `json:"include_paths,omitempty"`
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`
	Enabled *bool `json:"enabled,omitempty"`
}

// CodeWikiSyncRequest triggers repository sync. Empty repository_ids syncs
// every enabled binding under the KB's GitLab sources.
type CodeWikiSyncRequest struct {
	RepositoryIDs []string `json:"repository_ids,omitempty"`
}

// CodeWikiRunCreateRequest submits a generation run.
type CodeWikiRunCreateRequest struct {
	// IdempotencyKey deduplicates double submissions of the same intent
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// CodeWikiRunPublishRequest publishes a staged run. Baselines are verified
// server-side; ResolveConflicts tells the server how to handle pages whose
// live version moved past the generation baseline:
//
//	"abort"  (default) stop the publish, report conflicts
//	"keep"   keep the human-edited live page, mark candidate skipped
type CodeWikiRunPublishRequest struct {
	ResolveConflicts string `json:"resolve_conflicts,omitempty"`
}

// Publish conflict policies.
const (
	CodeWikiConflictAbort = "abort"
	CodeWikiConflictKeep  = "keep"
)

// Errors surfaced by the code wiki service layer.
var (
	ErrCodeWikiNotCodeWikiKB     = errors.New("knowledge base is not a code wiki knowledge base")
	ErrCodeWikiRepoNotFound      = errors.New("code wiki repository binding not found")
	ErrCodeWikiSnapshotNotFound  = errors.New("code wiki snapshot not found")
	ErrCodeWikiArtifactNotFound  = errors.New("code wiki analysis artifact not found")
	ErrCodeWikiProfileNotFound   = errors.New("code wiki analysis profile not found")
	ErrCodeWikiRunNotFound       = errors.New("code wiki run not found")
	ErrCodeWikiRunNotStaged      = errors.New("code wiki run is not staged")
	ErrCodeWikiRunActiveExists   = errors.New("another generation run is already active for this knowledge base")
	ErrCodeWikiNoRepositories    = errors.New("no enabled repositories with ready analysis artifacts")
	ErrCodeWikiSourceMismatch    = errors.New("data source does not belong to this knowledge base")
	ErrCodeWikiPublishConflict   = errors.New("publish conflict: live pages were edited after generation")
	ErrCodeWikiEvidenceNotFound  = errors.New("code wiki evidence not found")
	ErrCodeWikiProfileInvalid    = errors.New("code wiki analysis profile is invalid")
	ErrCodeWikiCapabilitiesMissing = errors.New("analysis provider does not meet minimum capabilities; run the capability test for details")
)

// CodeWikiRepositoryFilter is the query shape for listing bindings.
type CodeWikiRepositoryFilter struct {
	KnowledgeBaseID string
	SourceID string
	Enabled *bool
}

// CodeWikiCodeSnippet is a bounded source excerpt read from a snapshot,
// used for evidence validation and local source viewing.
type CodeWikiCodeSnippet struct {
	Path string `json:"path"`
	StartLine int `json:"start_line"`
	EndLine int `json:"end_line"`
	Content string `json:"content"`
	Truncated bool `json:"truncated,omitempty"`
}

// CodeWikiMaxSnippetLines bounds evidence excerpt reads.
const CodeWikiMaxSnippetLines = 60

// CodeWikiRunLeaseTTLSeconds bounds a worker's exclusive claim on a run;
// must exceed the longest allowed run timeout (MaxCodeWikiRunTimeoutMinutesCap)
// so a live worker's lease is never stolen, while a crashed worker's run
// becomes re-claimable (via user retry) afterwards.
const CodeWikiRunLeaseTTLSeconds = int(MaxCodeWikiRunTimeoutMinutesCap)*60 + 600

// FormatCodeWikiError keeps error text bounded for storage/display.
func FormatCodeWikiError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	return msg
}

// CodeWikiFilterHashInput is the stable serialization hashed into
// CodeWikiSnapshot.FilterHash.
func CodeWikiFilterHashInput(include []string, exclude []string) string {
	return fmt.Sprintf("inc=%s;exc=%s", strings.Join(include, ","), strings.Join(exclude, ","))
}
