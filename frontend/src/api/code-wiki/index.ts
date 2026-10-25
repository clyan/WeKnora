import { get, getDown, post, put, patch, del } from '../../utils/request';

export interface CodeWikiTemplate {
  id: string;
  content_instructions: string;
  extraction_instructions: string;
}

export const getCodeWikiTemplate = () => get<{ success: boolean; data: CodeWikiTemplate }>('/api/v1/code-wiki/templates');
export const exportCodeWikiOKF = (kbId: string) => getDown(`/api/v1/knowledgebase/${kbId}/code-wiki/export/okf`);

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
	ref?: string;
	filter_hash?: string;
	include_paths?: string[];
	exclude_patterns?: string[];
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
  | 'validating' | 'staged' | 'paused' | 'publishing' | 'published' | 'superseded'
  | 'failed' | 'canceled';

export interface CodeWikiRun {
  no_changes?: boolean;
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
  pause_requested: boolean;
  updated_at?: string;
  started_at?: string | null;
  finished_at?: string | null;
  published_at?: string | null;
  created_at: string;
}

export interface CodeWikiRunPage {
	baseline_content?: string;
	baseline_changed?: boolean;
  section_changes?: Array<{ id: string; title: string; before: string; after: string }>;
  id: string;
  run_id: string;
  page_key: string;
  title: string;
  page_type: string;
  category: string;
  category_path?: string[] | null;
  sort_order?: number;
  content: string;
  summary: string;
  evidence?: CodeWikiEvidenceSpec[] | null;
  baseline_version: number;
  validation_status: string;
  status: 'pending' | 'staged' | 'conflict' | 'published' | 'skipped' | 'preserved';
  generation_error?: string;
}

export interface CodeWikiRunDetail {
  run: CodeWikiRun;
  manifest: CodeWikiRunRepository[];
  pages: CodeWikiRunPage[] | null;
  events?: CodeWikiRunEvent[];
  worker_heartbeat_at?: string | null;
  baseline_run_id?: string;
  source_changes?: CodeWikiSourceChange[] | null;
  impact_plan?: CodeWikiImpactPlan | null;
}

export interface CodeWikiImpactPlan {
  plan_token: string;
  review_required?: boolean;
  review_approved?: boolean;
  new_pages?: Record<string, string>;
  sections?: Record<string, Array<{ id: string; title: string; hash: string }>>;
  impact?: Record<string, { kind: 'joint_review' | 'citation_changed' | 'scope_changed' | 'new_source'; changed_sources?: string[]; section_ids?: string[] }>;
}

export function approveCodeWikiImpact(kbId: string, runId: string, plan_token: string, sections: Record<string, string[]>) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/impact-review`, { plan_token, sections });
}

export interface CodeWikiRunEvent {
  id: number;
  kind: string;
  message: string;
  retry_at?: string;
  created_at: string;
}

export interface CodeWikiSourceChange {
  repository_id: string;
  path: string;
  kind: string;
  before_hash?: string;
  after_hash?: string;
}

export interface CodeWikiSourceEntry {
  name: string;
  path: string;
  is_directory: boolean;
  size: number;
}

export interface CodeWikiSourceDirectory {
  snapshot_id: string;
  commit_sha: string;
  prefix: string;
  entries: CodeWikiSourceEntry[];
  next_cursor?: string;
}

export interface CodeWikiSourcePage { page_key: string; title: string; version: number; matches_snapshot: boolean }
export function listCodeWikiSourcePages(kbId: string, repositoryId: string, params: {snapshot_id: string; path: string; after?: string}) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories/${repositoryId}/source-pages`, {params});
}

export function listCodeWikiSourceDirectory(kbId: string, repositoryId: string, params: { snapshot_id?: string; prefix?: string; after?: string }) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/repositories/${repositoryId}/files`, { params });
}

export function listCodeWikiRunEvents(kbId: string, runId: string, before = 0) {
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/events`, { params: { before } });
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

// 暂停：worker 在页面间隔观察到标志后进入 paused（计划与已完成页保留）。
export function pauseCodeWikiRun(kbId: string, runId: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/pause`);
}

// 继续：从持久化的计划接着生成剩余页面。
export function resumeCodeWikiRun(kbId: string, runId: string) {
  return post(`/api/v1/knowledgebase/${kbId}/code-wiki/runs/${runId}/resume`);
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

// 页面版本级引用列表（Wiki 阅读界面的代码来源侧栏）。version 缺省时
// 服务端解析为页面当前版本。
export function listPageEvidence(kbId: string, pageId: string, version?: number) {
  const qs = version ? `?version=${version}` : ''
  return get(`/api/v1/knowledgebase/${kbId}/code-wiki/pages/${pageId}/evidence${qs}`)
}
