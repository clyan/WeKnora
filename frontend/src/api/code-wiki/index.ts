import { get, post, put, patch, del } from '../../utils/request';

// Code Wiki 管理 API。
// 一个 code_wiki 知识库对应一个项目的多个 GitLab 仓库；分析由可替换的
// Provider（内置 codebase CLI 或自定义 MCP 服务）执行，产物为带代码
// 证据的 Wiki 文档。详见 docs/CODE_WIKI_IMPLEMENTATION_PLAN.md。

export type CodeWikiProviderKind = 'builtin_codebase' | 'custom_mcp';
export type CodeWikiSnapshotDelivery = 'local' | 'shared_dir' | 'import' | 'external_index';

export interface CodeWikiCapabilityStatus {
  capability: string;
  available: boolean;
  source?: string;
  tool?: string;
  detail?: string;
}

export interface CodeWikiCapabilityReport {
  checked_at: string;
  connected: boolean;
  connect_detail?: string;
  capabilities: CodeWikiCapabilityStatus[];
  blocking?: string[];
  notes?: string[];
  meets_minimum: boolean;
}

export interface CodeWikiToolMapping {
  capability: string;
  tool: string;
  input?: Record<string, string>;
  output?: {
    results_path?: string;
    path?: string;
    start_line?: string;
    end_line?: string;
    symbol?: string;
    snippet?: string;
    content?: string;
    text?: string;
    is_error?: string;
  };
}

export interface CodeWikiAnalysisProfile {
  id: string;
  knowledge_base_id: string;
  provider_kind: CodeWikiProviderKind;
  mcp_service_id?: string;
  tool_mappings?: CodeWikiToolMapping[] | null;
  snapshot_delivery: CodeWikiSnapshotDelivery;
  shared_root?: string;
  capability_check?: CodeWikiCapabilityReport | null;
  meets_minimum: boolean;
  config_version: number;
}

export interface CodeWikiRepositoryBinding {
  id: string;
  knowledge_base_id: string;
  source_id: string;
  project_id: string;
  display_name: string;
  web_url?: string;
  role: 'frontend' | 'backend' | 'service' | 'library' | 'other';
  ref: string;
  include_paths?: string[];
  exclude_patterns?: string[];
  enabled: boolean;
  status: 'idle' | 'syncing' | 'ready' | 'error';
  last_snapshot_id?: string | null;
  last_commit_sha?: string;
  last_sync_at?: string | null;
  error_message?: string;
}

export interface CodeWikiRunRepository {
  repository_id: string;
  snapshot_id?: string;
  artifact_id?: string;
  role: string;
  display_name: string;
  commit_sha?: string;
  excluded: boolean;
  exclude_reason?: string;
}

export interface CodeWikiEvidenceSpec {
  id: string;
  repository_id: string;
  snapshot_id: string;
  commit_sha?: string;
  path: string;
  start_line?: number;
  end_line?: number;
  symbol?: string;
  type?: string;
}

export type CodeWikiRunStatus =
  | 'pending' | 'preparing' | 'analyzing' | 'planning' | 'generating'
  | 'validating' | 'staged' | 'publishing' | 'published' | 'superseded'
  | 'failed' | 'canceled';

export interface CodeWikiRun {
  id: string;
  knowledge_base_id: string;
  status: CodeWikiRunStatus;
  phase?: string;
  model_id?: string;
  profile_version: number;
  total_pages: number;
  completed_pages: number;
  failed_pages: number;
  model_calls: number;
  tokens_used: number;
  error_message?: string;
  cancel_requested: boolean;
  started_at?: string | null;
  finished_at?: string | null;
  published_at?: string | null;
  created_at: string;
}

export interface CodeWikiRunPage {
  id: string;
  run_id: string;
  page_key: string;
  title: string;
  page_type: string;
  category: string;
  content: string;
  summary: string;
  evidence?: CodeWikiEvidenceSpec[] | null;
  baseline_version: number;
  validation_status: string;
  status: 'staged' | 'conflict' | 'published' | 'skipped';
  generation_error?: string;
}

export interface CodeWikiRunDetail {
  run: CodeWikiRun;
  manifest: CodeWikiRunRepository[];
  pages: CodeWikiRunPage[] | null;
}

export interface CodeWikiEvidenceView {
  id: string;
  page_id: string;
  page_version: number;
  run_id: string;
  evidence_key: string;
  repository_id: string;
  snapshot_id: string;
  commit_sha: string;
  path: string;
  start_line: number;
  end_line: number;
  symbol?: string;
  type?: string;
  repository_name?: string;
  repository_role?: string;
  web_url?: string;
  lines?: string;
}

// --- 分析 Provider 配置 -----------------------------------------------------

export function getCodeWikiAnalysisProfile(kbId: string) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/analysis-profile`);
}

export function updateCodeWikiAnalysisProfile(
  kbId: string,
  data: {
    provider_kind: CodeWikiProviderKind;
    mcp_service_id?: string;
    tool_mappings?: CodeWikiToolMapping[];
    snapshot_delivery?: CodeWikiSnapshotDelivery;
    shared_root?: string;
  },
) {
  return put(`/api/v1/knowledgebase/${kbId}/code-wiki/analysis-profile`, data);
}

// 分层能力检测：连接 → 映射 → 范围接受 → 最低能力。
export function testCodeWikiAnalysisProfile(kbId: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/analysis-profile/test`);
}

// --- 仓库绑定 ---------------------------------------------------------------

export function listCodeWikiRepositories(kbId: string) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories`);
}

export function createCodeWikiRepository(
  kbId: string,
  data: {
    source_id: string;
    project_id: string;
    display_name?: string;
    role?: string;
    ref?: string;
    include_paths?: string[];
    exclude_patterns?: string[];
    enabled?: boolean;
  },
) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories`, data);
}

export function updateCodeWikiRepository(
  kbId: string,
  id: string,
  data: {
    source_id: string;
    project_id: string;
    display_name?: string;
    role?: string;
    ref?: string;
    include_paths?: string[];
    exclude_patterns?: string[];
    enabled?: boolean;
  },
) {
  return patch(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories/${id}`, data);
}

export function deleteCodeWikiRepository(kbId: string, id: string) {
  return del(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories/${id}`);
}

// --- 同步与生成 --------------------------------------------------------------

// 异步：返回 202 与本次同步的仓库数量。
export function syncCodeWikiRepositories(kbId: string, repositoryIds?: string[]) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/sync`, {
    repository_ids: repositoryIds,
  });
}

export function listCodeWikiRuns(kbId: string) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/runs`);
}

// 异步：返回 202 与 run id；轮询 run 详情获取阶段进度。
export function createCodeWikiRun(kbId: string, idempotencyKey?: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs`, {
    idempotency_key: idempotencyKey,
  });
}

export function getCodeWikiRun(kbId: string, runId: string) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}`);
}

export function cancelCodeWikiRun(kbId: string, runId: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/cancel`);
}

export function retryCodeWikiRun(kbId: string, runId: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/retry`);
}

// resolve_conflicts: 'abort'（默认，冲突即暂停）| 'keep'（保留人工编辑，
// 对应候选页跳过）。
export function publishCodeWikiRun(kbId: string, runId: string, resolveConflicts: 'abort' | 'keep' = 'abort') {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/publish`, {
    resolve_conflicts: resolveConflicts,
  });
}

// --- 证据 -------------------------------------------------------------------

export function getCodeWikiEvidence(kbId: string, evidenceId: string) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/evidence/${evidenceId}`);
}
