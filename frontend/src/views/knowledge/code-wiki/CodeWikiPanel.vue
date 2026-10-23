<template>
  <div class="codewiki-panel">
    <!-- 分析服务 Provider -->
    <section class="cw-section">
      <div class="cw-section-header">
        <div>
          <h3 class="cw-section-title">{{ $t('codeWiki.provider.title') }}</h3>
          <p class="cw-section-desc">{{ $t('codeWiki.provider.description') }}</p>
        </div>
        <div class="cw-section-actions">
          <t-button variant="outline" size="small" :loading="testing" :disabled="!canEdit"
            @click="runCapabilityTest">
            {{ $t('codeWiki.provider.test') }}
          </t-button>
        </div>
      </div>

      <div class="provider-summary">
        <div class="provider-kind">
          <t-icon :name="profile?.provider_kind === 'custom_mcp' ? 'server' : 'terminal'" size="18px" />
          <div>
            <div class="provider-name">
              {{ profile?.provider_kind === 'custom_mcp' ? $t('codeWiki.provider.customMcp') : $t('codeWiki.provider.builtin') }}
            </div>
            <div class="provider-meta">
              {{ $t('codeWiki.provider.configVersion', { version: profile?.config_version ?? '-' }) }}
            </div>
          </div>
        </div>
        <div class="provider-capability">
          <t-tag v-if="capabilityReport?.meets_minimum" theme="success" variant="light" size="small">
            {{ $t('codeWiki.provider.meetsMinimum') }}
          </t-tag>
          <t-tag v-else-if="capabilityReport && capabilityReport.checked_at" theme="danger" variant="light" size="small">
            {{ $t('codeWiki.provider.missingCapabilities') }}
          </t-tag>
          <t-tag v-else theme="default" variant="light" size="small">
            {{ $t('codeWiki.provider.notTested') }}
          </t-tag>
          <t-button v-if="canEdit" variant="text" size="small" theme="primary" @click="providerDialogVisible = true">
            {{ $t('codeWiki.provider.configure') }}
          </t-button>
        </div>
      </div>

      <!-- 分层能力结果：连接 ≠ 映射有效 ≠ 可生成文档 -->
      <div v-if="capabilityReport?.capabilities?.length" class="capability-grid">
        <div v-for="cap in capabilityReport.capabilities" :key="cap.capability"
          :class="['capability-item', cap.available ? 'ok' : 'missing']">
          <t-icon :name="cap.available ? 'check-circle' : 'close-circle'" size="14px" />
          <span class="capability-name">{{ capabilityLabel(cap.capability) }}</span>
          <span v-if="!cap.available && cap.detail" class="capability-detail">{{ cap.detail }}</span>
        </div>
      </div>
      <p v-if="capabilityReport?.notes?.length" class="capability-notes">
        {{ capabilityReport.notes.join('；') }}
      </p>
    </section>

    <!-- 仓库绑定 -->
    <section class="cw-section">
      <div class="cw-section-header">
        <div>
          <h3 class="cw-section-title">{{ $t('codeWiki.repositories.title') }}</h3>
          <p class="cw-section-desc">{{ $t('codeWiki.repositories.description') }}</p>
        </div>
        <div class="cw-section-actions">
          <t-button variant="outline" size="small" :disabled="!canEdit || !repositories.length || anySyncing"
            @click="syncAllRepositories">
            {{ $t('codeWiki.repositories.syncAll') }}
          </t-button>
          <t-button size="small" :disabled="!canEdit" @click="repoDialogVisible = true">
            {{ $t('codeWiki.repositories.add') }}
          </t-button>
        </div>
      </div>

      <t-table v-if="repositories.length" row-key="id" :data="repositories" :columns="repoColumns" size="small"
        :loading="reposLoading">
        <template #status="{ row }">
          <t-tag v-if="row.status === 'ready'" theme="success" variant="light" size="small">
            {{ row.last_commit_sha ? row.last_commit_sha.slice(0, 8) : $t('codeWiki.status.ready') }}
          </t-tag>
          <t-tag v-else-if="row.status === 'syncing'" theme="primary" variant="light" size="small">
            <t-loading size="12px" style="margin-right: 4px" />{{ $t('codeWiki.status.syncing') }}
          </t-tag>
          <t-tag v-else-if="row.status === 'error'" theme="danger" variant="light" size="small">
            {{ $t('codeWiki.status.error') }}
          </t-tag>
          <t-tag v-else theme="default" variant="light" size="small">{{ $t('codeWiki.status.idle') }}</t-tag>
        </template>
        <template #error="{ row }">
          <t-tooltip v-if="row.error_message" :content="row.error_message" placement="top">
            <span class="repo-error">{{ row.error_message }}</span>
          </t-tooltip>
          <span v-else class="repo-error-empty">—</span>
        </template>
        <template #actions="{ row }">
          <t-button variant="text" size="small" theme="primary" :disabled="!canEdit || row.status === 'syncing'"
            @click="syncRepositories([row.id])">{{ $t('codeWiki.repositories.sync') }}</t-button>
          <t-popconfirm :content="$t('codeWiki.repositories.deleteConfirm')" @confirm="removeRepository(row)">
            <t-button variant="text" size="small" theme="danger" :disabled="!canEdit">
              {{ $t('common.delete') }}
            </t-button>
          </t-popconfirm>
        </template>
      </t-table>
      <div v-else-if="!reposLoading" class="cw-empty">
        <p>{{ $t('codeWiki.repositories.empty') }}</p>
        <t-button variant="outline" size="small" :disabled="!canEdit" @click="repoDialogVisible = true">
          {{ $t('codeWiki.repositories.add') }}
        </t-button>
      </div>
    </section>

    <!-- 生成批次 -->
    <section class="cw-section">
      <div class="cw-section-header">
        <div>
          <h3 class="cw-section-title">{{ $t('codeWiki.runs.title') }}</h3>
          <p class="cw-section-desc">{{ $t('codeWiki.runs.description') }}</p>
        </div>
        <div class="cw-section-actions">
          <template v-if="activeRun">
            <!-- staged: publish, or discard the candidates -->
            <template v-if="activeRun.status === 'staged'">
              <t-button size="small" theme="primary" :disabled="!canEdit" @click="publishActiveRun">
                {{ $t('codeWiki.runs.publish') }}
              </t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.discard') }}</t-button>
            </template>
            <!-- paused: resume or cancel -->
            <template v-else-if="activeRun.status === 'paused'">
              <t-button size="small" theme="primary" :disabled="!canEdit" @click="resumeActiveRun">
                {{ $t('codeWiki.runs.resume') }}
              </t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.cancel') }}</t-button>
            </template>
            <!-- in flight: pause or cancel -->
            <template v-else>
              <t-button variant="outline" size="small" :disabled="!canEdit"
                @click="pauseActiveRun">{{ $t('codeWiki.runs.pause') }}</t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.cancel') }}</t-button>
            </template>
          </template>
          <t-button v-else size="small" theme="primary" :disabled="!canEdit || !readyRepositoryCount"
            @click="createRun">
            {{ $t('codeWiki.runs.create') }}
          </t-button>
        </div>
      </div>

      <!-- 活动批次进度：阶段文字而非百分比 -->
      <div v-if="activeRun" :class="['run-progress', activeRun.status]">
        <div class="run-progress-main">
          <t-tag size="small" variant="light" :theme="runStatusTheme(activeRun.status)">
            {{ runStatusLabel(activeRun.status) }}
          </t-tag>
          <span class="run-phase">{{ activeRun.phase || runStatusLabel(activeRun.status) }}</span>
          <span v-if="activeRun.total_pages" class="run-pages">
            {{ activeRun.completed_pages }}/{{ activeRun.total_pages }}
          </span>
          <span v-if="activeRun.status === 'staged' && stagedConflictCount > 0" class="run-conflict-hint">
            {{ $t('codeWiki.runs.conflicts', { count: stagedConflictCount }) }}
          </span>
        </div>
        <div v-if="activeRun.status === 'staged'" class="run-progress-actions">
          <t-radio-group v-model="publishConflictPolicy" variant="default-radio" size="small">
            <t-radio value="abort">{{ $t('codeWiki.runs.conflictAbort') }}</t-radio>
            <t-radio value="keep">{{ $t('codeWiki.runs.conflictKeep') }}</t-radio>
          </t-radio-group>
        </div>
        <p v-if="activeRun.error_message" class="run-error">{{ activeRun.error_message }}</p>
        <!-- Per-page progress: planned pages show as pending before generation
             reaches them; failures carry the concrete reason. -->
        <ul v-if="runPages.length" class="run-page-list">
          <li v-for="p in runPages" :key="p.id" class="run-page-item">
            <span class="run-page-name">{{ p.title || p.page_key }}</span>
            <t-tag v-if="p.generation_error" size="small" variant="light" theme="danger">
              {{ $t('codeWiki.runs.pageFailed') }}
            </t-tag>
            <t-tag v-else-if="p.status === 'pending'" size="small" variant="light" theme="default">
              {{ $t('codeWiki.runs.pagePending') }}
            </t-tag>
            <t-tag v-else size="small" variant="light" theme="success">
              {{ $t('codeWiki.runs.pageDone') }}
            </t-tag>
            <t-tooltip v-if="p.generation_error" :content="p.generation_error" placement="top">
              <span class="run-page-error">{{ p.generation_error }}</span>
            </t-tooltip>
          </li>
        </ul>
      </div>

      <t-table v-if="runs.length" row-key="id" :data="runs" :columns="runColumns" size="small" :loading="runsLoading">
        <template #status="{ row }">
          <t-tag size="small" variant="light" :theme="runStatusTheme(row.status)">
            {{ runStatusLabel(row.status) }}
          </t-tag>
        </template>
        <template #run-actions="{ row }">
          <t-button v-if="row.status === 'failed' || row.status === 'canceled'" variant="text" size="small"
            theme="primary" :disabled="!!activeRun || !canEdit" @click="retryRun(row)">
            {{ $t('codeWiki.runs.retry') }}
          </t-button>
        </template>
      </t-table>
    </section>

    <!-- 添加仓库弹窗 -->
    <CodeWikiRepositoryDialog v-model:visible="repoDialogVisible" :knowledge-base-id="knowledgeBaseId"
      :data-sources="gitlabSources" :editing="null" @created="onRepositoryCreated" />

    <!-- Provider 配置弹窗 -->
    <CodeWikiProviderDialog v-model:visible="providerDialogVisible" :knowledge-base-id="knowledgeBaseId"
      :profile="profile" @saved="onProviderSaved" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  listCodeWikiRepositories, syncCodeWikiRepositories, deleteCodeWikiRepository,
  listCodeWikiRuns, createCodeWikiRun, cancelCodeWikiRun, pauseCodeWikiRun,
  resumeCodeWikiRun, retryCodeWikiRun, publishCodeWikiRun, getCodeWikiRun,
  getCodeWikiAnalysisProfile, testCodeWikiAnalysisProfile,
} from '@/api/code-wiki'
import type {
  CodeWikiAnalysisProfile, CodeWikiCapabilityReport, CodeWikiRepositoryBinding, CodeWikiRun,
  CodeWikiRunPage,
} from '@/api/code-wiki'
import { listDataSources } from '@/api/datasource'
import CodeWikiRepositoryDialog from './CodeWikiRepositoryDialog.vue'
import CodeWikiProviderDialog from './CodeWikiProviderDialog.vue'

const props = defineProps<{
  knowledgeBaseId: string
  canEdit: boolean
}>()

const emit = defineEmits<{
  (e: 'open-kb-settings'): void
}>()

const { t } = useI18n()

const profile = ref<CodeWikiAnalysisProfile | null>(null)
const repositories = ref<CodeWikiRepositoryBinding[]>([])
const runs = ref<CodeWikiRun[]>([])
const gitlabSources = ref<any[]>([])
const reposLoading = ref(false)
const runsLoading = ref(false)
const testing = ref(false)
const repoDialogVisible = ref(false)
const providerDialogVisible = ref(false)
const publishConflictPolicy = ref<'abort' | 'keep'>('abort')

let pollTimer: ReturnType<typeof setInterval> | null = null

const capabilityReport = computed<CodeWikiCapabilityReport | null>(() => profile.value?.capability_check ?? null)
const activeRun = computed<CodeWikiRun | null>(() => {
  const terminal = ['published', 'superseded', 'failed', 'canceled']
  return runs.value.find((run) => !terminal.includes(run.status)) ?? null
})
const readyRepositoryCount = computed(() => repositories.value.filter((r) => r.status === 'ready').length)
// A syncing repository disables the sync buttons; the backend also refuses
// double submissions, this keeps the UI from even offering them.
const anySyncing = computed(() => repositories.value.some((r) => r.status === 'syncing'))
const stagedConflictCount = computed(() => {
  // Conflict detail lives on the run detail pages payload; surfaced during
  // publish errors. Kept as a reactive ref updated by publish failures.
  return stagedConflicts.value
})
const stagedConflicts = ref(0)

const capabilityLabels: Record<string, string> = {}
const capabilityLabel = (key: string) =>
  capabilityLabels[key] ?? t(`codeWiki.capabilities.${key}`)

// --- data loading -----------------------------------------------------------

const loadProfile = async () => {
  try {
    const res: any = await getCodeWikiAnalysisProfile(props.knowledgeBaseId)
    profile.value = res?.data ?? res
  } catch {
    // KB may predate the profile API (older deployment); leave null.
  }
}

const loadRepositories = async () => {
  reposLoading.value = true
  try {
    const res: any = await listCodeWikiRepositories(props.knowledgeBaseId)
    repositories.value = res?.data ?? res ?? []
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    reposLoading.value = false
  }
}

const loadRuns = async () => {
  runsLoading.value = true
  try {
    const res: any = await listCodeWikiRuns(props.knowledgeBaseId)
    runs.value = res?.data ?? res ?? []
  } catch {
    // silent: run list refresh is best-effort
  } finally {
    runsLoading.value = false
  }
}

// Per-page progress for the active run (planned pages appear as pending
// before generation reaches them; errors carry the per-page reason).
const runPages = ref<CodeWikiRunPage[]>([])
let runDetailSeq = 0
const loadRunDetail = async () => {
  const seq = ++runDetailSeq
  const run = activeRun.value
  if (!run) {
    runPages.value = []
    return
  }
  try {
    const res: any = await getCodeWikiRun(props.knowledgeBaseId, run.id)
    if (seq !== runDetailSeq) return
    const detail = res?.data ?? res
    runPages.value = detail?.pages ?? []
  } catch {
    // best-effort: the strip degrades to stage + counters
  }
}

const loadGitlabSources = async () => {
  try {
    const res: any = await listDataSources(props.knowledgeBaseId)
    const list = res?.data ?? res ?? []
    gitlabSources.value = list.filter((ds: any) => ds.type === 'gitlab')
  } catch {
    gitlabSources.value = []
  }
}

const refreshAll = async () => {
  await Promise.all([loadProfile(), loadRepositories(), loadRuns(), loadGitlabSources()])
  loadRunDetail()
}

onMounted(() => {
  refreshAll()
  // Poll while any repository is syncing or a run is active.
  pollTimer = setInterval(() => {
    const syncing = repositories.value.some((r) => r.status === 'syncing')
    if (syncing || activeRun.value) {
      loadRepositories()
      loadRuns()
      loadRunDetail()
    }
  }, 5000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})

// --- provider ---------------------------------------------------------------

const runCapabilityTest = async () => {
  testing.value = true
  try {
    const res: any = await testCodeWikiAnalysisProfile(props.knowledgeBaseId)
    profile.value = res?.data ?? res
    const report = profile.value?.capability_check
    if (report?.meets_minimum) {
      MessagePlugin.success(t('codeWiki.provider.meetsMinimum'))
    } else {
      MessagePlugin.warning(t('codeWiki.provider.missingCapabilities'))
    }
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    testing.value = false
  }
}

const onProviderSaved = (saved: CodeWikiAnalysisProfile) => {
  profile.value = saved
  MessagePlugin.info(t('codeWiki.provider.savedRetest'))
}

// --- repositories -----------------------------------------------------------

const onRepositoryCreated = () => {
  repoDialogVisible.value = false
  loadRepositories()
}

const syncRepositories = async (ids: string[]) => {
  try {
    await syncCodeWikiRepositories(props.knowledgeBaseId, ids)
    MessagePlugin.success(t('codeWiki.repositories.syncSubmitted'))
    setTimeout(loadRepositories, 800)
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const syncAllRepositories = () => syncRepositories([])

const removeRepository = async (row: CodeWikiRepositoryBinding) => {
  try {
    await deleteCodeWikiRepository(props.knowledgeBaseId, row.id)
    MessagePlugin.success(t('common.deleted'))
    loadRepositories()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

// --- runs -------------------------------------------------------------------

const createRun = async () => {
  try {
    await createCodeWikiRun(props.knowledgeBaseId)
    MessagePlugin.success(t('codeWiki.runs.created'))
    loadRuns()
    loadRunDetail()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const cancelActiveRun = async () => {
  if (!activeRun.value) return
  try {
    await cancelCodeWikiRun(props.knowledgeBaseId, activeRun.value.id)
    MessagePlugin.success(t('codeWiki.runs.cancelRequested'))
    loadRuns()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const pauseActiveRun = async () => {
  if (!activeRun.value) return
  try {
    await pauseCodeWikiRun(props.knowledgeBaseId, activeRun.value.id)
    MessagePlugin.success(t('codeWiki.runs.pauseRequested'))
    loadRuns()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const resumeActiveRun = async () => {
  if (!activeRun.value) return
  try {
    await resumeCodeWikiRun(props.knowledgeBaseId, activeRun.value.id)
    MessagePlugin.success(t('codeWiki.runs.resumed'))
    loadRuns()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const retryRun = async (row: CodeWikiRun) => {
  try {
    await retryCodeWikiRun(props.knowledgeBaseId, row.id)
    MessagePlugin.success(t('codeWiki.runs.created'))
    loadRuns()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  }
}

const publishActiveRun = async () => {
  if (!activeRun.value) return
  try {
    await publishCodeWikiRun(props.knowledgeBaseId, activeRun.value.id, publishConflictPolicy.value)
    stagedConflicts.value = 0
    MessagePlugin.success(t('codeWiki.runs.published'))
    loadRuns()
  } catch (err: any) {
    const message: string = err?.message ?? ''
    if (message.includes('publish paused') || message.includes('conflict')) {
      stagedConflicts.value = 1
      MessagePlugin.warning(message || t('codeWiki.runs.conflictHint'))
    } else {
      MessagePlugin.error(message || t('codeWiki.loadFailed'))
    }
  }
}

// --- table columns ----------------------------------------------------------

const repoColumns = computed(() => [
  { colKey: 'display_name', title: t('codeWiki.repositories.columnName'), ellipsis: true },
  { colKey: 'role', title: t('codeWiki.repositories.columnRole'), width: 110,
    cell: (_h: any, { row }: any) => t(`codeWiki.roles.${row.role}`) },
  { colKey: 'ref', title: t('codeWiki.repositories.columnRef'), width: 110, ellipsis: true },
  { colKey: 'status', title: t('codeWiki.repositories.columnStatus'), width: 130 },
  { colKey: 'error', title: t('codeWiki.repositories.columnError'), ellipsis: true },
  { colKey: 'actions', title: t('common.actions'), width: 150 },
])

const runColumns = computed(() => [
  { colKey: 'created_at', title: t('codeWiki.runs.columnTime'), width: 170,
    cell: (_h: any, { row }: any) => new Date(row.created_at).toLocaleString() },
  { colKey: 'status', title: t('codeWiki.runs.columnStatus'), width: 110 },
  { colKey: 'completed_pages', title: t('codeWiki.runs.columnPages'), width: 90,
    cell: (_h: any, { row }: any) => `${row.completed_pages}/${row.total_pages}` },
  { colKey: 'model_calls', title: t('codeWiki.runs.columnModelCalls'), width: 100 },
  { colKey: 'run-actions', title: t('common.actions'), width: 100 },
])

const runStatusLabel = (status: string) => t(`codeWiki.runStatus.${status}`)
const runStatusTheme = (status: string): any => {
  switch (status) {
    case 'published': return 'success'
    case 'failed': return 'danger'
    case 'canceled': case 'superseded': return 'default'
    case 'staged': case 'paused': return 'warning'
    default: return 'primary'
  }
}
</script>

<style scoped>
.codewiki-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 4px 0 24px;
}

.cw-section {
  background: var(--td-bg-color-container);
  border-radius: var(--td-radius-large);
  padding: 20px 24px;
}

.cw-section-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 14px;
}

.cw-section-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.cw-section-desc {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.cw-section-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.provider-summary {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  padding: 12px 0;
}

.provider-kind {
  display: flex;
  align-items: center;
  gap: 10px;
}

.provider-name {
  font-size: 14px;
  font-weight: 500;
}

.provider-meta {
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.provider-capability {
  display: flex;
  align-items: center;
  gap: 8px;
}

.capability-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 6px 16px;
  margin-top: 4px;
}

.capability-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}

.capability-item.ok {
  color: var(--td-success-color);
}

.capability-item.missing {
  color: var(--td-text-color-secondary);
}

.capability-name {
  color: var(--td-text-color-primary);
}

.capability-detail {
  color: var(--td-text-color-placeholder);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.capability-notes {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--td-warning-color);
}

.cw-empty {
  text-align: center;
  padding: 24px 0;
  color: var(--td-text-color-placeholder);
  font-size: 13px;
}

.repo-error {
  color: var(--td-error-color);
  font-size: 12px;
  cursor: default;
}

.repo-error-empty {
  color: var(--td-text-color-placeholder);
}

.run-progress {
  border: 1px solid var(--td-component-border);
  border-radius: var(--td-radius-medium);
  padding: 12px 16px;
  margin-bottom: 12px;
}

.run-progress-main {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.run-phase {
  font-size: 13px;
  color: var(--td-text-color-primary);
}

.run-pages {
  font-size: 12px;
  color: var(--td-text-color-secondary);
}

.run-conflict-hint {
  font-size: 12px;
  color: var(--td-warning-color);
}

.run-progress-actions {
  margin-top: 8px;
}

.run-error {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--td-error-color);
}

.run-page-list {
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.run-page-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  min-width: 0;
}

.run-page-name {
  flex: none;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.run-page-error {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--td-error-color);
  opacity: 0.85;
  cursor: default;
}
</style>
