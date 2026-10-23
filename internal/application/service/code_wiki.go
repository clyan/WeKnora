package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/datasource/connector/gitlab"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// codeWikiService implements interfaces.CodeWikiService: analysis provider
// profiles, repository bindings, snapshot sync, generation runs and
// publication. See docs/CODE_WIKI_IMPLEMENTATION_PLAN.md.
type codeWikiService struct {
	repo         interfaces.CodeWikiRepository
	kbService    interfaces.KnowledgeBaseService
	dsService    interfaces.DataSourceService
	mcpSvcRepo   interfaces.MCPServiceRepository
	registry     *codebase.Registry
	taskEnqueuer interfaces.TaskEnqueuer
	wikiService  interfaces.WikiPageService
	modelService interfaces.ModelService
	syncLogRepo  interfaces.SyncLogRepository
	audit        interfaces.AuditLogService
}

// NewCodeWikiService wires the service. dig resolves every dependency from
// the container.
func NewCodeWikiService(
	repo interfaces.CodeWikiRepository,
	kbService interfaces.KnowledgeBaseService,
	dsService interfaces.DataSourceService,
	mcpSvcRepo interfaces.MCPServiceRepository,
	registry *codebase.Registry,
	taskEnqueuer interfaces.TaskEnqueuer,
	wikiService interfaces.WikiPageService,
	modelService interfaces.ModelService,
	syncLogRepo interfaces.SyncLogRepository,
	audit interfaces.AuditLogService,
) interfaces.CodeWikiService {
	return &codeWikiService{
		repo:         repo,
		kbService:    kbService,
		dsService:    dsService,
		mcpSvcRepo:   mcpSvcRepo,
		registry:     registry,
		taskEnqueuer: taskEnqueuer,
		wikiService:  wikiService,
		modelService: modelService,
		syncLogRepo:  syncLogRepo,
		audit:        audit,
	}
}

// requireCodeWikiKB loads the KB and enforces the code_wiki type. All entry
// points run through this so a code-wiki operation can never execute
// against a document/faq/wiki KB.
func (s *codeWikiService) requireCodeWikiKB(ctx context.Context, kbID string) (*types.KnowledgeBase, error) {
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if kb == nil || !kb.IsCodeWiki() {
		return nil, apperrors.NewBadRequestError("knowledge base is not a code wiki knowledge base")
	}
	return kb, nil
}

func codeWikiTenantID(ctx context.Context) uint64 {
	return types.MustTenantIDFromContext(ctx)
}

// --- Analysis provider profile ----------------------------------------------

// GetAnalysisProfile returns the KB's provider configuration, creating the
// default builtin profile on first access.
func (s *codeWikiService) GetAnalysisProfile(ctx context.Context, kbID string) (*types.CodeWikiAnalysisProfile, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	profile, err := s.repo.GetAnalysisProfile(ctx, codeWikiTenantID(ctx), kbID)
	if err != nil {
		return nil, err
	}
	// Refresh the builtin report after a local executable upgrade. Historical
	// runs keep their frozen report; the current configuration reflects reality.
	if profile.ProviderKind == "" || profile.ProviderKind == types.CodeWikiProviderBuiltin {
		return s.TestAnalysisProfile(ctx, kbID)
	}
	return profile, nil
}

// UpdateAnalysisProfile validates and saves the provider configuration.
// Saving increments the config version — in-flight and historical runs keep
// the version they froze. The profile is NOT marked usable here: the caller
// must run TestAnalysisProfile (plan §4.4: 切换服务或映射 → 新配置版本 +
// 重新检测能力 + 重新准备分析产物).
func (s *codeWikiService) UpdateAnalysisProfile(ctx context.Context, kbID string, req *types.CodeWikiAnalysisProfileRequest) (*types.CodeWikiAnalysisProfile, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if !types.IsValidCodeWikiProviderKind(req.ProviderKind) {
		return nil, apperrors.NewBadRequestError(fmt.Sprintf("unknown provider kind %q", req.ProviderKind))
	}
	profile, err := s.repo.GetAnalysisProfile(ctx, kb.TenantID, kbID)
	if err != nil {
		return nil, err
	}

	// Validate provider-specific fields before persisting.
	switch req.ProviderKind {
	case types.CodeWikiProviderCustomMCP:
		if strings.TrimSpace(req.MCPServiceID) == "" {
			return nil, apperrors.NewBadRequestError("custom_mcp provider requires an MCP service reference")
		}
		// The referenced service must exist in this tenant — a raw service_id
		// from another tenant is rejected here, not silently accepted.
		service, svcErr := s.mcpSvcRepo.GetByID(ctx, kb.TenantID, req.MCPServiceID)
		if svcErr != nil || service == nil {
			return nil, apperrors.NewBadRequestError("MCP service not found in this workspace")
		}
		if len(req.ToolMappings) > 0 {
			if err := codebase.ValidateToolMappings(req.ToolMappings); err != nil {
				return nil, apperrors.NewBadRequestError(err.Error())
			}
		}
	default:
		// builtin provider ignores MCP-specific fields.
		req.MCPServiceID = ""
		req.ToolMappings = nil
	}
	delivery := req.SnapshotDelivery
	if delivery == "" {
		if req.ProviderKind == types.CodeWikiProviderCustomMCP {
			delivery = types.CodeWikiDeliverySharedDir
		} else {
			delivery = types.CodeWikiDeliveryLocal
		}
	}
	if !types.IsValidCodeWikiDelivery(delivery) {
		return nil, apperrors.NewBadRequestError(fmt.Sprintf("unknown snapshot delivery %q", req.SnapshotDelivery))
	}
	if req.ProviderKind == types.CodeWikiProviderBuiltin && delivery != types.CodeWikiDeliveryLocal {
		return nil, apperrors.NewBadRequestError("builtin provider only supports local snapshot delivery")
	}
	if delivery == types.CodeWikiDeliverySharedDir && strings.TrimSpace(req.SharedRoot) == "" {
		return nil, apperrors.NewBadRequestError("shared_dir delivery requires a shared root directory")
	}

	if blob, err := json.Marshal(req.ToolMappings); err == nil && req.ToolMappings != nil {
		profile.ToolMappings = types.JSON(blob)
	} else {
		profile.ToolMappings = nil
	}
	profile.ProviderKind = req.ProviderKind
	profile.MCPServiceID = req.MCPServiceID
	profile.SnapshotDelivery = delivery
	profile.SharedRoot = strings.TrimRight(strings.TrimSpace(req.SharedRoot), "/")
	// Changing provider/mapping invalidates the previous capability check
	// until re-tested.
	profile.CapabilityCheck = nil
	profile.MeetsMinimum = false
	profile.ConfigVersion++

	if err := s.repo.SaveAnalysisProfile(ctx, profile); err != nil {
		return nil, err
	}
	// Keep the denormalized version pointer on the KB config in sync.
	if kb.CodeWikiConfig == nil {
		kb.CodeWikiConfig = &types.CodeWikiConfig{}
	}
	kb.CodeWikiConfig.AnalysisProfileVersion = profile.ConfigVersion
	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
		"code_wiki_profile", profile.ID, types.AuditOutcomeSuccess,
		map[string]any{"provider_kind": profile.ProviderKind, "config_version": profile.ConfigVersion})
	return profile, nil
}

// TestAnalysisProfile runs the layered capability check and persists the
// report: connectivity → tool mapping → scope acceptance → per-capability
// availability → minimum capability verdict (plan §4.3: 每次检测需区分
// "连接成功""映射有效""快照可用"和"可生成文档").
func (s *codeWikiService) TestAnalysisProfile(ctx context.Context, kbID string) (*types.CodeWikiAnalysisProfile, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	profile, err := s.repo.GetAnalysisProfile(ctx, kb.TenantID, kbID)
	if err != nil {
		return nil, err
	}
	provider, err := s.registry.Resolve(ctx, profile)
	if err != nil {
		// Resolution failure (service disabled/missing) is itself a check
		// result the UI must show, not a 500.
		report := &types.CodeWikiCapabilityReport{
			CheckedAt:     time.Now().UTC(),
			Connected:     false,
			ConnectDetail: err.Error(),
			MeetsMinimum:  false,
		}
		persistCodeWikiCapabilityReport(ctx, s.repo, profile, report)
		return profile, nil
	}
	report, err := provider.CheckCapabilities(ctx, profile)
	if err != nil {
		return nil, err
	}
	persistCodeWikiCapabilityReport(ctx, s.repo, profile, report)
	return profile, nil
}

func persistCodeWikiCapabilityReport(ctx context.Context, repo interfaces.CodeWikiRepository, profile *types.CodeWikiAnalysisProfile, report *types.CodeWikiCapabilityReport) {
	blob, err := json.Marshal(report)
	if err != nil {
		return
	}
	profile.CapabilityCheck = types.JSON(blob)
	profile.MeetsMinimum = report.MeetsMinimum
	if err := repo.SaveAnalysisProfile(ctx, profile); err != nil {
		logger.Warnf(ctx, "code wiki: persist capability report failed: %v", err)
	}
}

// --- Repository bindings ----------------------------------------------------

func (s *codeWikiService) ListRepositories(ctx context.Context, kbID string) ([]*types.CodeWikiRepository, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	return s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{KnowledgeBaseID: kbID})
}

// CreateRepository validates and inserts a repository binding. The source
// must be a GitLab data source under the same tenant AND same KB — a
// binding referencing another KB's connection is rejected (plan §3.1).
func (s *codeWikiService) CreateRepository(ctx context.Context, kbID string, req *types.CodeWikiRepositoryUpsertRequest) (*types.CodeWikiRepository, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	count, err := s.repo.CountRepositories(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if count >= types.MaxCodeWikiRepositoriesPerKB {
		return nil, apperrors.NewBadRequestError(fmt.Sprintf("a code wiki knowledge base supports at most %d repositories", types.MaxCodeWikiRepositoriesPerKB))
	}
	ds, includePaths, excludePatterns, err := s.validateBindingRequest(ctx, kb, req)
	if err != nil {
		return nil, err
	}

	// Resolve the project now so display name/web URL default from real
	// metadata and a nonexistent project fails fast (recorded as a 400 with
	// the concrete GitLab error, not a later background failure).
	client, err := s.gitLabClientFor(ctx, ds)
	if err != nil {
		return nil, err
	}
	info, err := client.GetProject(ctx, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return nil, apperrors.NewBadRequestError(fmt.Sprintf("cannot access GitLab project: %v", err))
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = info.PathWithNamespace
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = info.DefaultBranch
	}

	repo := &types.CodeWikiRepository{
		ID:              uuid.NewString(),
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kbID,
		SourceID:        ds.ID,
		ProjectID:       strings.TrimSpace(req.ProjectID),
		DisplayName:     displayName,
		WebURL:          info.WebURL,
		Role:            normalizedCodeWikiRole(req.Role),
		Ref:             ref,
		IncludePaths:    includePaths,
		ExcludePatterns: excludePatterns,
		Enabled:         true,
		Status:          types.CodeWikiRepoStatusIdle,
	}
	if req.Enabled != nil {
		repo.Enabled = *req.Enabled
	}
	if err := s.repo.CreateRepository(ctx, repo); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionDataSourceCreated,
		"code_wiki_repository", repo.ID, types.AuditOutcomeSuccess,
		map[string]any{"project": repo.ProjectID, "ref": repo.Ref, "role": repo.Role})
	return repo, nil
}

func (s *codeWikiService) UpdateRepository(ctx context.Context, kbID, id string, req *types.CodeWikiRepositoryUpsertRequest) (*types.CodeWikiRepository, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	repo, err := s.repo.GetRepository(ctx, kbID, id)
	if err != nil {
		return nil, err
	}
	ds, includePaths, excludePatterns, err := s.validateBindingRequest(ctx, kb, req)
	if err != nil {
		return nil, err
	}
	filtersChanged := !stringSlicesEqual(repo.IncludePaths, includePaths) || !stringSlicesEqual(repo.ExcludePatterns, excludePatterns)
	refChanged := strings.TrimSpace(req.Ref) != "" && strings.TrimSpace(req.Ref) != repo.Ref

	repo.SourceID = ds.ID
	repo.ProjectID = strings.TrimSpace(req.ProjectID)
	if strings.TrimSpace(req.DisplayName) != "" {
		repo.DisplayName = strings.TrimSpace(req.DisplayName)
	}
	repo.Role = normalizedCodeWikiRole(req.Role)
	if strings.TrimSpace(req.Ref) != "" {
		repo.Ref = strings.TrimSpace(req.Ref)
	}
	repo.IncludePaths = includePaths
	repo.ExcludePatterns = excludePatterns
	if req.Enabled != nil {
		repo.Enabled = *req.Enabled
	}
	if err := s.repo.UpdateRepository(ctx, repo); err != nil {
		return nil, err
	}
	// Ref/filter changes require a new snapshot: surface that by resetting
	// the binding's readiness instead of silently reusing the old snapshot.
	if filtersChanged || refChanged {
		repo.Status = types.CodeWikiRepoStatusIdle
		repo.ErrorMessage = "configuration changed; re-sync required"
		_ = s.repo.UpdateRepositoryStatus(ctx, repo)
	}
	return repo, nil
}

func (s *codeWikiService) DeleteRepository(ctx context.Context, kbID, id string) error {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return err
	}
	repo, err := s.repo.GetRepository(ctx, kbID, id)
	if err != nil {
		return err
	}
	// Mark stale before delete so the UI shows why docs went stale; snapshot
	// disk cleanup is deferred to retention (historical evidence must keep
	// resolving, plan §3.2).
	if err := s.repo.DeleteRepository(ctx, kbID, id); err != nil {
		return err
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionDataSourceDeleted,
		"code_wiki_repository", repo.ID, types.AuditOutcomeSuccess,
		map[string]any{"project": repo.ProjectID})
	return nil
}

// validateBindingRequest normalizes and validates the request's source
// reference and filters, returning the resolved data source.
func (s *codeWikiService) validateBindingRequest(ctx context.Context, kb *types.KnowledgeBase, req *types.CodeWikiRepositoryUpsertRequest) (*types.DataSource, types.StringArray, types.StringArray, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, nil, nil, apperrors.NewBadRequestError("project_id is required")
	}
	ds, err := s.dsService.GetDataSource(ctx, strings.TrimSpace(req.SourceID))
	if err != nil || ds == nil {
		return nil, nil, nil, apperrors.NewBadRequestError("data source not found")
	}
	if ds.TenantID != kb.TenantID || ds.KnowledgeBaseID != kb.ID {
		return nil, nil, nil, apperrors.NewBadRequestError("data source does not belong to this knowledge base")
	}
	if ds.Type != types.ConnectorTypeGitLab {
		return nil, nil, nil, apperrors.NewBadRequestError("code wiki repository bindings require a GitLab data source")
	}
	includePaths, err := normalizeCodeWikiPaths(req.IncludePaths, types.MaxCodeWikiIncludePathsPerRepo, true)
	if err != nil {
		return nil, nil, nil, apperrors.NewBadRequestError(err.Error())
	}
	excludePatterns, err := normalizeCodeWikiPaths(req.ExcludePatterns, types.MaxCodeWikiExcludePatternsPerRepo, false)
	if err != nil {
		return nil, nil, nil, apperrors.NewBadRequestError(err.Error())
	}
	return ds, includePaths, excludePatterns, nil
}

// normalizeCodeWikiPaths trims, dedupes and bounds path/glob lists. Include
// paths must be plain directory roots (no glob metacharacters) — they define
// the snapshot's filter scope and its reuse hash.
func normalizeCodeWikiPaths(values []string, max int, strictRoots bool) (types.StringArray, error) {
	out := make(types.StringArray, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		v := strings.Trim(strings.TrimSpace(value), "/")
		if v == "" {
			continue
		}
		if v == "." || v == ".." || strings.HasPrefix(v, "../") || strings.Contains(v, "/../") || strings.Contains(v, "\\") {
			return nil, fmt.Errorf("invalid path %q", value)
		}
		if strictRoots && strings.ContainsAny(v, "*?[") {
			return nil, fmt.Errorf("include_paths must be directory roots without wildcards (got %q)", value)
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
		if len(out) > max {
			return nil, fmt.Errorf("too many entries (max %d)", max)
		}
	}
	return out, nil
}

func normalizedCodeWikiRole(role string) string {
	role = strings.TrimSpace(role)
	if !types.IsValidCodeWikiRepoRole(role) {
		return types.CodeWikiRepoRoleOther
	}
	return role
}

func stringSlicesEqual(a, b types.StringArray) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gitLabClientFor builds a code client from the data source's decrypted
// credentials. Failure to decrypt/configure surfaces as a user-facing
// error, never as a credential leak.
func (s *codeWikiService) gitLabClientFor(ctx context.Context, ds *types.DataSource) (*gitlab.CodeClient, error) {
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return nil, apperrors.NewBadRequestError("data source configuration is invalid")
	}
	baseURL, _ := config.Credentials["base_url"].(string)
	token, _ := config.Credentials["access_token"].(string)
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(token) == "" {
		return nil, apperrors.NewBadRequestError("GitLab credentials are not configured for this data source")
	}
	client, err := gitlab.NewCodeClient(baseURL, token)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	return client, nil
}

// snapshotRelPath builds the on-disk layout under the data root:
// <tenant>/<kb>/<snapshot>. Forward-slash separators keep the layout
// portable and match the shared-dir mapping contract.
func snapshotRelPath(tenantID uint64, kbID, snapshotID string) string {
	return path.Join(fmt.Sprint(tenantID), kbID, snapshotID)
}
