package codebase

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tenantFromCtx extracts the tenant id from the standard context key.
func tenantFromCtx(ctx context.Context) (uint64, bool) {
	if v, ok := ctx.Value(types.TenantIDContextKey).(uint64); ok {
		return v, true
	}
	return 0, false
}

// Registry resolves a KB's analysis profile into a live provider instance.
// The builtin adapter is a shared singleton; custom MCP providers are built
// per call so a disabled/withdrawn service fails the pipeline instead of
// being silently reused (plan §4.4: "服务被禁用、授权撤销或不再可验证时停止执行").
type Registry struct {
	builtin *BuiltinProvider
	mcpMgr  *mcp.MCPManager
	mcpRepo interfaces.MCPServiceRepository
}

// NewRegistry wires the resolver. mcpMgr/mcpRepo may be nil in builds
// without MCP support — custom profiles then fail resolution with a clear
// error instead of panicking.
func NewRegistry(mcpMgr *mcp.MCPManager, mcpRepo interfaces.MCPServiceRepository) *Registry {
	return &Registry{
		builtin: NewBuiltinProvider(),
		mcpMgr:  mcpMgr,
		mcpRepo: mcpRepo,
	}
}

// Builtin exposes the shared builtin adapter (also used for local source
// reads even when a custom provider serves analysis).
func (r *Registry) Builtin() *BuiltinProvider { return r.builtin }

// Resolve builds the provider for one profile.
func (r *Registry) Resolve(ctx context.Context, profile *types.CodeWikiAnalysisProfile) (CodeAnalysisProvider, error) {
	if profile == nil {
		return nil, types.ErrCodeWikiProfileNotFound
	}
	switch profile.ProviderKind {
	case "", types.CodeWikiProviderBuiltin:
		return r.builtin, nil
	case types.CodeWikiProviderCustomMCP:
		if r.mcpMgr == nil || r.mcpRepo == nil {
			return nil, fmt.Errorf("codebase: MCP support unavailable; cannot use custom_mcp provider")
		}
		if profile.MCPServiceID == "" {
			return nil, fmt.Errorf("codebase: custom_mcp provider requires an MCP service reference")
		}
		tenantID, ok := tenantFromCtx(ctx)
		if !ok {
			return nil, fmt.Errorf("codebase: tenant context required to resolve MCP service")
		}
		service, err := r.mcpRepo.GetByID(ctx, tenantID, profile.MCPServiceID)
		if err != nil || service == nil {
			return nil, fmt.Errorf("codebase: MCP service %s not found: %w", profile.MCPServiceID, err)
		}
		if !service.Enabled {
			return nil, fmt.Errorf("codebase: MCP service %q is disabled", service.Name)
		}
		mappings, err := profile.ParsedToolMappings()
		if err != nil {
			return nil, fmt.Errorf("codebase: decode tool mappings: %w", err)
		}
		delivery := profile.SnapshotDelivery
		if delivery == "" || delivery == types.CodeWikiDeliveryLocal {
			// Custom MCP services cannot read WeKnora-local paths; default
			// them to shared_dir so the misconfiguration is visible.
			delivery = types.CodeWikiDeliverySharedDir
		}
		return NewMCPProvider(service, r.mcpMgr, mappings, delivery, profile.SharedRoot)
	default:
		return nil, fmt.Errorf("codebase: unknown provider kind %q", profile.ProviderKind)
	}
}
