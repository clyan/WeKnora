package codebase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
)

// MCPProvider adapts a tenant-configured MCP service (SSE / HTTP
// Streamable) as a code analysis provider through declarative capability→
// tool mappings. Connection, authentication (API key / bearer / OAuth),
// timeouts and tool enable/approval policy are owned by the existing MCP
// service stack — this adapter only calls ListTools/CallTool.
//
// Scope enforcement: every request renders {{repo}}/{{commit}}/{{handle}}
// from the server-frozen scope into the mapped tool inputs, and the
// capability check refuses services whose mapped tools cannot accept
// repository/commit parameters (see CheckCapabilities). The plan is
// explicit that filtering foreign hits out of an unscoped search is NOT
// acceptable isolation — providers that cannot scope are rejected.
type MCPProvider struct {
	kind        string
	mcpService  *types.MCPService
	mcpManager  *mcp.MCPManager
	mappings    map[string]types.CodeWikiToolMapping
	delivery    string
	sharedRoot  string
}

// NewMCPProvider builds the adapter for one KB profile snapshot. The
// service entity is resolved at construction so a profile version pins the
// service it was validated against; callers re-resolve per task run so a
// disabled service stops the pipeline (plan §4.4).
func NewMCPProvider(
	service *types.MCPService,
	manager *mcp.MCPManager,
	mappings []types.CodeWikiToolMapping,
	delivery string,
	sharedRoot string,
) (*MCPProvider, error) {
	if service == nil {
		return nil, fmt.Errorf("codebase: custom MCP provider requires an MCP service")
	}
	if manager == nil {
		return nil, fmt.Errorf("codebase: MCP manager is required")
	}
	if err := ValidateToolMappings(mappings); err != nil {
		return nil, err
	}
	byCapability := make(map[string]types.CodeWikiToolMapping, len(mappings))
	for _, m := range mappings {
		byCapability[m.Capability] = m
	}
	if delivery == "" {
		delivery = types.CodeWikiDeliverySharedDir
	}
	return &MCPProvider{
		kind:       types.CodeWikiProviderCustomMCP,
		mcpService: service,
		mcpManager: manager,
		mappings:   byCapability,
		delivery:   delivery,
		sharedRoot: strings.TrimRight(strings.TrimSpace(sharedRoot), "/"),
	}, nil
}

func (p *MCPProvider) Kind() string { return p.kind }

// mapping returns the mapping for a capability.
func (p *MCPProvider) mapping(capability string) (types.CodeWikiToolMapping, bool) {
	m, ok := p.mappings[capability]
	return m, ok
}

// client returns a connected MCP client, failing closed when the service is
// disabled or unreachable.
func (p *MCPProvider) client(ctx context.Context) (mcp.MCPClient, error) {
	client, err := p.mcpManager.GetOrCreateClient(ctx, p.mcpService)
	if err != nil {
		return nil, fmt.Errorf("codebase: MCP service %s: %w", p.mcpService.Name, err)
	}
	return client, nil
}

// EngineVersion derives a stable "engine version" from the service identity
// so artifact versioning stays meaningful for remote services. Real engine
// versions, when the service reports them via tool results, are recorded in
// the artifact's coverage instead.
func (p *MCPProvider) EngineVersion(ctx context.Context) (string, error) {
	return "mcp:" + p.mcpService.ID[:8], nil
}

// callTool executes a mapped tool and returns its decoded JSON content.
// Text-only results are wrapped as {"text": …} so output mappings with a
// TextField still work.
func (p *MCPProvider) callTool(ctx context.Context, m types.CodeWikiToolMapping, mc mappingContext) (interface{}, error) {
	client, err := p.client(ctx)
	if err != nil {
		return nil, err
	}
	args := renderInput(m.Input, mc)
	result, err := client.CallTool(ctx, m.Tool, args)
	if err != nil {
		return nil, fmt.Errorf("codebase: tool %s: %w", m.Tool, err)
	}
	if result == nil {
		return nil, fmt.Errorf("codebase: tool %s: empty result", m.Tool)
	}
	if result.IsError {
		return nil, fmt.Errorf("codebase: tool %s returned an error: %s", m.Tool, firstText(result))
	}
	return decodeToolContent(result)
}

func firstText(result *mcp.CallToolResult) string {
	for _, item := range result.Content {
		if item.Type == "text" && item.Text != "" {
			return truncateTail(item.Text, 500)
		}
	}
	return "(no detail)"
}

// decodeToolContent merges the tool's content items into one decoded JSON
// value: a single JSON text item decodes as-is; structured items are merged
// into a map; a plain text item becomes {"text": …}.
func decodeToolContent(result *mcp.CallToolResult) (interface{}, error) {
	var textParts []string
	for _, item := range result.Content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			textParts = append(textParts, item.Text)
		}
	}
	if len(textParts) == 1 {
		trimmed := strings.TrimSpace(textParts[0])
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded interface{}
			if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
				return decoded, nil
			}
		}
		return map[string]interface{}{"text": textParts[0]}, nil
	}
	if len(textParts) > 1 {
		return map[string]interface{}{"text": strings.Join(textParts, "\n")}, nil
	}
	return map[string]interface{}{}, nil
}

// CheckCapabilities performs the layered check: connection → tool presence
// → scope acceptance → per-capability availability. A service whose search
// tool cannot receive repository/commit scope fails the required
// snapshot_scope capability and is rejected.
func (p *MCPProvider) CheckCapabilities(ctx context.Context, profile *types.CodeWikiAnalysisProfile) (*types.CodeWikiCapabilityReport, error) {
	report := &types.CodeWikiCapabilityReport{CheckedAt: time.Now().UTC()}
	client, err := p.client(ctx)
	if err != nil {
		report.ConnectDetail = err.Error()
		return report, nil
	}
	report.Connected = true

	tools, err := client.ListTools(ctx)
	if err != nil {
		report.Connected = false
		report.ConnectDetail = fmt.Sprintf("list tools: %v", err)
		return report, nil
	}
	available := make(map[string]bool, len(tools))
	for _, t := range tools {
		available[t.Name] = true
	}

	has := func(capability string) (types.CodeWikiToolMapping, bool) {
		m, ok := p.mapping(capability)
		if !ok {
			return m, false
		}
		if !available[m.Tool] {
			return m, false
		}
		return m, true
	}
	set := func(status types.CodeWikiCapabilityStatus) {
		report.Capabilities = append(report.Capabilities, status)
	}

	// Required: snapshot scope — the search/prepare mappings must reference
	// repo/commit/handle variables so queries can be pinned. A tool that
	// only accepts a mutable branch is unusable for publishable docs.
	scopeOK := true
	scopeDetail := ""
	for _, capability := range []string{types.CodeWikiCapCodeSearch, types.CodeWikiCapSnapshotPrepare} {
		m, ok := has(capability)
		if !ok {
			continue
		}
		if !mappingBindsScope(m) {
			scopeOK = false
			scopeDetail = fmt.Sprintf("mapped %s tool %q does not bind {{repo}}/{{commit}}/{{handle}}; queries cannot be pinned to a snapshot", capability, m.Tool)
		}
	}
	set(types.CodeWikiCapabilityStatus{
		Capability: types.CodeWikiCapSnapshotScope, Available: scopeOK, Source: "provider",
		Detail: scopeDetail,
	})

	if m, ok := has(types.CodeWikiCapCodeSearch); ok {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapCodeSearch, Available: true, Source: "provider", Tool: m.Tool})
	} else {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapCodeSearch, Available: false, Detail: "no code_search mapping to an available tool"})
	}

	// Source read: provider tool or WeKnora's local snapshot copy.
	if m, ok := has(types.CodeWikiCapSourceRead); ok {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSourceRead, Available: true, Source: "provider", Tool: m.Tool})
	} else {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSourceRead, Available: true, Source: "local", Detail: "source excerpts read from WeKnora's local snapshot"})
	}

	if m, ok := has(types.CodeWikiCapSnapshotPrepare); ok {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSnapshotPrepare, Available: true, Source: "provider", Tool: m.Tool})
	} else if p.delivery == types.CodeWikiDeliveryExternalIndex {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSnapshotPrepare, Available: false, Detail: "external index binding must be verified per snapshot; map snapshot_prepare to the service's verify tool"})
	} else {
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSnapshotPrepare, Available: false, Detail: "no snapshot_prepare mapping; map the service's index/import/verify tool"})
	}

	// Optional capabilities.
	optional := []string{types.CodeWikiCapDirectory, types.CodeWikiCapSymbols, types.CodeWikiCapCallGraph, types.CodeWikiCapCrossRepo, types.CodeWikiCapCoverage}
	for _, capability := range optional {
		if m, ok := has(capability); ok {
			set(types.CodeWikiCapabilityStatus{Capability: capability, Available: true, Source: "provider", Tool: m.Tool})
		} else {
			set(types.CodeWikiCapabilityStatus{Capability: capability, Available: false, Detail: "not mapped; analysis degrades accordingly"})
		}
	}
	if !report.HasCapability(types.CodeWikiCapCallGraph) {
		report.Notes = append(report.Notes, "no call graph: relations are only described when confirmed by source reads")
	}
	if !report.HasCapability(types.CodeWikiCapCrossRepo) {
		report.Notes = append(report.Notes, "no native cross-repository capability: associations formed by per-repository retrieval plus source verification")
	}
	if p.delivery == types.CodeWikiDeliverySharedDir && p.sharedRoot == "" {
		report.Notes = append(report.Notes, "shared_dir delivery configured without a shared root; snapshots cannot be delivered")
		set(types.CodeWikiCapabilityStatus{Capability: types.CodeWikiCapSnapshotPrepare, Available: false, Detail: "shared root not configured"})
	}
	for _, capability := range types.RequiredCodeWikiCapabilities {
		if !report.HasCapability(capability) {
			report.Blocking = append(report.Blocking, capability)
		}
	}
	report.MeetsMinimum = len(report.Blocking) == 0
	return report, nil
}

// mappingBindsScope reports whether a mapping's inputs reference at least
// one scope-pinning variable.
func mappingBindsScope(m types.CodeWikiToolMapping) bool {
	for _, value := range m.Input {
		v := strings.TrimSpace(value)
		if v == "{{repo}}" || v == "{{commit}}" || v == "{{handle}}" {
			return true
		}
	}
	return false
}

// sharedSnapshotPath maps a WeKnora snapshot into the configured shared
// root. Only paths under the root are ever handed to the service.
func (p *MCPProvider) sharedSnapshotPath(scope SnapshotScope) (string, bool) {
	if p.delivery != types.CodeWikiDeliverySharedDir || p.sharedRoot == "" || scope.LocalPath == "" {
		return "", false
	}
	// The snapshot root layout is <dataRoot>/<tenant>/<kb>/<snapshot>; the
	// shared root maps 1:1 onto <dataRoot>. We only forward the suffix.
	marker := "/code-wiki/"
	idx := strings.Index(scope.LocalPath, marker)
	if idx < 0 {
		return "", false
	}
	suffix := strings.TrimPrefix(scope.LocalPath[idx+len(marker)-1:], "/")
	return p.sharedRoot + "/" + suffix, true
}

// PrepareArtifact imports or verifies the snapshot with the service's
// mapped tool. What "prepare" means depends on the delivery method:
// shared_dir passes the shared path, import passes handle verification,
// external_index verifies the recorded repository/commit scope.
func (p *MCPProvider) PrepareArtifact(ctx context.Context, scope SnapshotScope) (*ArtifactResult, error) {
	m, ok := p.mapping(types.CodeWikiCapSnapshotPrepare)
	if !ok {
		return nil, ErrUnsupported
	}
	sharedPath, hasShared := p.sharedSnapshotPath(scope)
	if p.delivery == types.CodeWikiDeliverySharedDir && !hasShared {
		return nil, fmt.Errorf("codebase: snapshot %s has no shared-dir mapping (delivery=shared_dir)", scope.SnapshotID)
	}
	mc := mappingContext{
		Repo:   scope.RepositoryID,
		Commit: scope.CommitSHA,
		Handle: scope.ArtifactHandle,
		Path:   sharedPath,
		Limit:  1,
	}
	result, err := p.callTool(ctx, m, mc)
	if err != nil {
		return nil, err
	}
	// The prepare tool's response may carry a handle; fall back to the
	// deterministic project name when it does not.
	handle := scope.ProjectID()
	if node, ok := lookupPath(result, "handle"); ok {
		if s := coerceString(node); s != "" {
			handle = s
		}
	}
	version, _ := p.EngineVersion(ctx)
	var coverage types.JSON
	if raw, err := json.Marshal(result); err == nil {
		coverage = types.JSON(raw)
	}
	return &ArtifactResult{Handle: handle, EngineVersion: version, Ready: true, Coverage: coverage}, nil
}

// SearchCode runs the mapped code_search tool and standardizes hits.
func (p *MCPProvider) SearchCode(ctx context.Context, scope SnapshotScope, query string, opts QueryOptions) ([]SearchHit, error) {
	m, ok := p.mapping(types.CodeWikiCapCodeSearch)
	if !ok {
		return nil, ErrUnsupported
	}
	mc := mappingContext{
		Repo:   scope.RepositoryID,
		Commit: scope.CommitSHA,
		Query:  query,
		Handle: scope.ArtifactHandle,
		Limit:  normalizeLimit(opts.Limit),
		Offset: opts.Offset,
	}
	result, err := p.callTool(ctx, m, mc)
	if err != nil {
		return nil, err
	}
	return extractSearchHits(result, m.Output)
}

// extractSearchHits projects a decoded tool response into standardized hits
// using the mapping's output paths. Structured results take priority; a
// text blob yields a single pseudo-hit whose snippet carries the text.
func extractSearchHits(result interface{}, out types.CodeWikiMappingOutput) ([]SearchHit, error) {
	if out.ResultsPath != "" {
		items, ok := lookupPath(result, out.ResultsPath)
		if !ok {
			return nil, fmt.Errorf("codebase: results path %q missing from tool response", out.ResultsPath)
		}
		list, ok := items.([]interface{})
		if !ok {
			return nil, fmt.Errorf("codebase: results path %q is not an array", out.ResultsPath)
		}
		hits := make([]SearchHit, 0, len(list))
		for _, item := range list {
			var hit SearchHit
			if node, ok := lookupPath(item, out.PathField); ok {
				hit.Path = coerceString(node)
			}
			if node, ok := lookupPath(item, out.StartLineField); ok {
				hit.StartLine = coerceInt(node)
			}
			if node, ok := lookupPath(item, out.EndLineField); ok {
				hit.EndLine = coerceInt(node)
			}
			if node, ok := lookupPath(item, out.SymbolField); ok {
				hit.Symbol = coerceString(node)
			}
			if node, ok := lookupPath(item, out.SnippetField); ok {
				hit.Snippet = coerceString(node)
			}
			if hit.Path != "" {
				hits = append(hits, hit)
			}
		}
		return hits, nil
	}
	if out.TextField != "" {
		if node, ok := lookupPath(result, out.TextField); ok {
			text := coerceString(node)
			if text != "" {
				return []SearchHit{{Snippet: text}}, nil
			}
		}
	}
	if node, ok := lookupPath(result, "text"); ok {
		if text := coerceString(node); text != "" {
			return []SearchHit{{Snippet: text}}, nil
		}
	}
	return nil, nil
}

// ReadSource uses the mapped source_read tool when present; otherwise the
// service layer falls back to the local snapshot (see ReadSource contract).
func (p *MCPProvider) ReadSource(ctx context.Context, scope SnapshotScope, path string, startLine, endLine int) (string, error) {
	m, ok := p.mapping(types.CodeWikiCapSourceRead)
	if !ok {
		return "", ErrUnsupported
	}
	mc := mappingContext{
		Repo:      scope.RepositoryID,
		Commit:    scope.CommitSHA,
		Path:      path,
		Handle:    scope.ArtifactHandle,
		StartLine: startLine,
		EndLine:   endLine,
	}
	result, err := p.callTool(ctx, m, mc)
	if err != nil {
		return "", err
	}
	if out := m.Output; out.ContentField != "" {
		if node, ok := lookupPath(result, out.ContentField); ok {
			return coerceString(node), nil
		}
	}
	if node, ok := lookupPath(result, "text"); ok {
		return coerceString(node), nil
	}
	return "", nil
}

// callOptional runs a mapped optional capability and returns the decoded
// response, or ErrUnsupported when unmapped.
func (p *MCPProvider) callOptional(ctx context.Context, capability string, mc mappingContext) (interface{}, error) {
	m, ok := p.mapping(capability)
	if !ok {
		return nil, ErrUnsupported
	}
	return p.callTool(ctx, m, mc)
}

func (p *MCPProvider) ListDirectory(ctx context.Context, scope SnapshotScope, pathPrefix string) ([]DirectoryEntry, error) {
	result, err := p.callOptional(ctx, types.CodeWikiCapDirectory, mappingContext{
		Repo: scope.RepositoryID, Commit: scope.CommitSHA, Handle: scope.ArtifactHandle, Path: pathPrefix,
	})
	if err != nil {
		return nil, err
	}
	m, _ := p.mapping(types.CodeWikiCapDirectory)
	if m.Output.ResultsPath == "" {
		return nil, ErrUnsupported
	}
	items, ok := lookupPath(result, m.Output.ResultsPath)
	if !ok {
		return nil, nil
	}
	list, ok := items.([]interface{})
	if !ok {
		return nil, nil
	}
	out := make([]DirectoryEntry, 0, len(list))
	for _, item := range list {
		var entry DirectoryEntry
		if node, ok := lookupPath(item, m.Output.PathField); ok {
			entry.Path = coerceString(node)
		}
		if entry.Path == "" {
			continue
		}
		if node, ok := lookupPath(item, "is_dir"); ok {
			entry.IsDir = coerceString(node) == "true" || coerceInt(node) == 1
		}
		out = append(out, entry)
	}
	return out, nil
}

func (p *MCPProvider) QuerySymbols(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]SymbolHit, error) {
	m, ok := p.mapping(types.CodeWikiCapSymbols)
	if !ok {
		return nil, ErrUnsupported
	}
	result, err := p.callOptional(ctx, types.CodeWikiCapSymbols, mappingContext{
		Repo: scope.RepositoryID, Commit: scope.CommitSHA, Handle: scope.ArtifactHandle,
		Symbol: symbol, Limit: normalizeLimit(opts.Limit), Offset: opts.Offset,
	})
	if err != nil {
		return nil, err
	}
	if m.Output.ResultsPath == "" {
		return nil, ErrUnsupported
	}
	items, _ := lookupPath(result, m.Output.ResultsPath)
	list, _ := items.([]interface{})
	out := make([]SymbolHit, 0, len(list))
	for _, item := range list {
		var hit SymbolHit
		if node, ok := lookupPath(item, m.Output.SymbolField); ok {
			hit.Symbol = coerceString(node)
		}
		if node, ok := lookupPath(item, m.Output.PathField); ok {
			hit.Path = coerceString(node)
		}
		if node, ok := lookupPath(item, m.Output.StartLineField); ok {
			hit.StartLine = coerceInt(node)
		}
		if node, ok := lookupPath(item, m.Output.EndLineField); ok {
			hit.EndLine = coerceInt(node)
		}
		if hit.Symbol != "" || hit.Path != "" {
			out = append(out, hit)
		}
	}
	return out, nil
}

func (p *MCPProvider) QueryRelations(ctx context.Context, scope SnapshotScope, symbol string, opts QueryOptions) ([]RelationEdge, error) {
	m, ok := p.mapping(types.CodeWikiCapCallGraph)
	if !ok {
		return nil, ErrUnsupported
	}
	result, err := p.callOptional(ctx, types.CodeWikiCapCallGraph, mappingContext{
		Repo: scope.RepositoryID, Commit: scope.CommitSHA, Handle: scope.ArtifactHandle,
		Symbol: symbol, Limit: normalizeLimit(opts.Limit), Offset: opts.Offset,
	})
	if err != nil {
		return nil, err
	}
	if m.Output.ResultsPath == "" {
		return nil, ErrUnsupported
	}
	items, _ := lookupPath(result, m.Output.ResultsPath)
	list, _ := items.([]interface{})
	out := make([]RelationEdge, 0, len(list))
	for _, item := range list {
		var edge RelationEdge
		edge.Kind = "candidate"
		if node, ok := lookupPath(item, "kind"); ok {
			edge.Kind = coerceString(node)
		}
		if node, ok := lookupPath(item, "from_symbol"); ok {
			edge.FromSymbol = coerceString(node)
		}
		if node, ok := lookupPath(item, "from_path"); ok {
			edge.FromPath = coerceString(node)
		}
		if node, ok := lookupPath(item, "to_symbol"); ok {
			edge.ToSymbol = coerceString(node)
		}
		if node, ok := lookupPath(item, "to_path"); ok {
			edge.ToPath = coerceString(node)
		}
		if node, ok := lookupPath(item, "start_line"); ok {
			edge.StartLine = coerceInt(node)
		}
		out = append(out, edge)
	}
	return out, nil
}

// QueryCrossRepository runs the mapped cross-repo tool with the frozen
// repository set. Scopes are passed via {{handle}} joined list so the
// service cannot widen the query beyond the run manifest.
func (p *MCPProvider) QueryCrossRepository(ctx context.Context, scopes []SnapshotScope, query string, opts QueryOptions) ([]RelationEdge, error) {
	m, ok := p.mapping(types.CodeWikiCapCrossRepo)
	if !ok {
		return nil, ErrUnsupported
	}
	handles := make([]string, 0, len(scopes))
	for _, s := range scopes {
		handles = append(handles, s.ArtifactHandle)
	}
	result, err := p.callOptional(ctx, types.CodeWikiCapCrossRepo, mappingContext{
		Query: query, Handle: strings.Join(handles, ","), Limit: normalizeLimit(opts.Limit),
	})
	if err != nil {
		return nil, err
	}
	if m.Output.ResultsPath == "" {
		return nil, ErrUnsupported
	}
	items, _ := lookupPath(result, m.Output.ResultsPath)
	list, _ := items.([]interface{})
	out := make([]RelationEdge, 0, len(list))
	for _, item := range list {
		var edge RelationEdge
		edge.Kind = "candidate"
		if node, ok := lookupPath(item, "kind"); ok {
			edge.Kind = coerceString(node)
		}
		if node, ok := lookupPath(item, "to_symbol"); ok {
			edge.ToSymbol = coerceString(node)
		}
		if node, ok := lookupPath(item, "to_path"); ok {
			edge.ToPath = coerceString(node)
		}
		if node, ok := lookupPath(item, "external_target"); ok {
			edge.ExternalTarget = coerceString(node)
		}
		out = append(out, edge)
	}
	return out, nil
}

func (p *MCPProvider) ReportCoverage(ctx context.Context, scope SnapshotScope) (*CoverageReport, error) {
	if _, ok := p.mapping(types.CodeWikiCapCoverage); !ok {
		return nil, ErrUnsupported
	}
	result, err := p.callOptional(ctx, types.CodeWikiCapCoverage, mappingContext{
		Repo: scope.RepositoryID, Commit: scope.CommitSHA, Handle: scope.ArtifactHandle,
	})
	if err != nil {
		return nil, err
	}
	report := &CoverageReport{}
	if node, ok := lookupPath(result, "engine_version"); ok {
		report.EngineVersion = coerceString(node)
	}
	if node, ok := lookupPath(result, "languages"); ok {
		if list, ok := node.([]interface{}); ok {
			for _, item := range list {
				report.Languages = append(report.Languages, coerceString(item))
			}
		}
	}
	if node, ok := lookupPath(result, "indexed_files"); ok {
		report.IndexedFiles = coerceInt(node)
	}
	if node, ok := lookupPath(result, "total_files"); ok {
		report.TotalFiles = coerceInt(node)
	}
	return report, nil
}

// CleanupArtifact refuses: remote artifacts' ownership cannot be verified
// through a generic mapping, so cleanup is never issued blindly. Operators
// clean external services through their own tooling.
func (p *MCPProvider) CleanupArtifact(ctx context.Context, handle string) error {
	return ErrUnsupported
}
