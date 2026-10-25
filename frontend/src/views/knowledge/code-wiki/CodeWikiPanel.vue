<template>
  <div class="codewiki-panel">
    <div class="workspace-toolbar">
      <t-button variant="outline" size="small" @click="openTask()">
        <template #icon>
          <t-loading v-if="activeRun && isExecuting(activeRun.status)" size="14px" />
          <t-icon v-else name="task" />
        </template>
        {{ activeRun ? (activeRun.phase || runStatusLabel(activeRun.status)) : (runs[0] ? runStatusLabel(runs[0].status) : $t('codeWiki.runs.title')) }}
        <span v-if="activeRun?.total_pages"> · {{ activeRun.completed_pages }}/{{ activeRun.total_pages }}</span>
      </t-button>
      <t-button v-if="canEdit" variant="text" size="small" @click="sourcesVisible = true">
        {{ $t('codeWiki.workspace.manageSources') }}
      </t-button>
      <t-button variant="text" size="small" :loading="exportingOKF" @click="downloadOKF">
        {{ $t('codeWiki.template.export') }}
      </t-button>
    </div>

    <section v-if="showSources" class="cw-section">
      <div class="cw-section-header">
        <div>
          <h3 class="cw-section-title">{{ $t('codeWiki.workspace.sourceFiles') }}</h3>
          <p class="cw-section-desc">{{ $t('codeWiki.workspace.sourceHint') }}</p>
        </div>
        <t-button size="small" :disabled="!canEdit" @click="sourcesVisible = true">
          {{ $t('codeWiki.workspace.manageSources') }}
        </t-button>
      </div>
      <div class="source-grid">
        <div v-for="repo in repositories" :key="repo.id" class="source-card">
          <button type="button" class="source-card-main" @click="openSourceRepository(repo)">
            <strong>{{ repo.display_name }}</strong>
            <span>{{ $t(`codeWiki.roles.${repo.role}`) }} · {{ repo.ref || $t('codeWiki.workspace.defaultBranch') }}</span>
            <span>{{ $t(`codeWiki.status.${repo.status}`) }} · {{ repo.last_commit_sha?.slice(0, 8) || '—' }}</span>
          </button>
          <button v-if="repo.error_message" type="button" class="repo-error" @click="syncErrorRepository = repo">
            {{ repo.error_message }} · {{ $t('codeWiki.repositories.errorDetails') }}
          </button>
        </div>
      </div>
      <p v-if="!repositories.length && !reposLoading" class="cw-empty">{{ $t('codeWiki.repositories.empty') }}</p>
      <button type="button" class="task-card" @click="openTask()">
        <strong>{{ $t('codeWiki.workspace.taskDetails') }}</strong>
        <span>{{ activeRun?.phase || (runs[0] ? runStatusLabel(runs[0].status) : $t('codeWiki.workspace.noTask')) }}</span>
        <span>{{ $t('codeWiki.workspace.viewProgress') }} →</span>
      </button>
    </section>

    <SettingDrawer v-model:visible="sourceTreeVisible" :title="sourceRepository?.display_name || $t('codeWiki.workspace.sourceFiles')"
      icon="folder" width="760px" :min-width="320" hide-footer>
      <p class="cw-section-desc">{{ $t('codeWiki.workspace.sourceTreeHint') }}</p>
      <p v-if="sourceDirectory" class="run-manifest">{{ sourceDirectory.commit_sha }}</p>
      <nav class="source-breadcrumb" :aria-label="$t('codeWiki.workspace.sourceFiles')">
        <button type="button" class="page-link" @click="loadSourceDirectory('')">{{ $t('codeWiki.workspace.sourceRoot') }}</button>
        <button v-for="(segment, index) in sourcePrefix.split('/').filter(Boolean)" :key="index" type="button" class="page-link"
          @click="loadSourceDirectory(sourcePrefix.split('/').slice(0, index + 1).join('/'))"> / {{ segment }}</button>
      </nav>
      <t-loading v-if="sourceTreeLoading" size="small" />
      <p v-if="sourceTreeError" class="run-error" role="status">{{ sourceTreeError }}</p>
      <ul v-if="sourceDirectory" class="source-file-list">
        <li v-for="entry in sourceDirectory.entries" :key="entry.path">
          <button type="button" class="page-link" :aria-pressed="selectedSourceFile?.path === entry.path"
            @click="entry.is_directory ? loadSourceDirectory(entry.path) : selectSourceFile(entry)">
            <t-icon :name="entry.is_directory ? 'folder' : 'file'" /> {{ entry.name }}
          </button>
          <span v-if="!entry.is_directory" class="cw-section-desc">{{ entry.size }} B</span>
        </li>
      </ul>
      <t-button v-if="sourceDirectory?.next_cursor" variant="text" :disabled="sourceTreeLoading" @click="loadSourceDirectory(sourcePrefix, true)">
        {{ $t('codeWiki.workspace.moreFiles') }}
      </t-button>
      <div v-if="selectedSourceFile" class="candidate-detail">
        <h4>{{ selectedSourceFile.path }}</h4>
        <a v-if="selectedSourceFileUrl" class="page-link" :href="selectedSourceFileUrl" target="_blank" rel="noopener noreferrer">
          {{ $t('codeWiki.workspace.viewFrozenSource') }}
        </a>
        <p class="cw-section-desc">{{ $t('codeWiki.workspace.fileStatusHint') }}</p>
        <h4>{{ $t('codeWiki.workspace.publishedReferences') }}</h4>
        <t-loading v-if="sourcePagesLoading" size="small" />
        <p v-if="sourcePagesError" class="run-error" role="status">{{ sourcePagesError }}</p>
        <ul>
          <li v-for="page in publishedSourcePages" :key="page.page_key">
            <button type="button" class="page-link" @click="sourceTreeVisible = false; emit('open-wiki-page', page.page_key)">
              {{ page.title }} · v{{ page.version }}
            </button>
            <span v-if="!page.matches_snapshot" class="cw-section-desc"> · {{ $t('codeWiki.workspace.historicalSource') }}</span>
          </li>
        </ul>
        <t-button v-if="sourcePagesCursor" variant="text" :disabled="sourcePagesLoading" @click="loadSourcePages(true)">{{ $t('codeWiki.workspace.morePages') }}</t-button>
        <ul v-if="sourceRelatedPages.length">
          <li v-for="page in sourceRelatedPages" :key="page.id">
            <button type="button" class="page-link" @click="taskVisible = true; sourceTreeVisible = false; openDocumentPreview(page.page_key)">
              {{ page.title }} · {{ page.generation_error ? $t('codeWiki.runs.pageFailed') : page.status === 'pending' ? $t('codeWiki.runs.pagePending') : $t('codeWiki.runs.pageDone') }}
            </button>
          </li>
        </ul>
      </div>
    </SettingDrawer>

    <SettingDrawer v-model:visible="sourcesVisible" :title="$t('codeWiki.workspace.manageSources')"
      icon="folder" width="760px" :min-width="320" hide-footer @update:visible="onSourcesVisibility">
    <DataSourceSettings :kb-id="knowledgeBaseId" kb-type="code_wiki" @count="loadGitlabSources" />
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
          <t-button size="small" :disabled="!canEdit" @click="editRepository()">
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
            <button type="button" class="repo-error" @click="syncErrorRepository = row">{{ row.error_message }}</button>
          </t-tooltip>
          <span v-else class="repo-error-empty">—</span>
        </template>
        <template #actions="{ row }">
          <t-button variant="text" size="small" :disabled="!canEdit || row.status === 'syncing'"
            @click="editRepository(row)">{{ $t('common.edit') }}</t-button>
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
        <t-button variant="outline" size="small" :disabled="!canEdit" @click="editRepository()">
          {{ $t('codeWiki.repositories.add') }}
        </t-button>
      </div>
    </section>
    </SettingDrawer>

    <!-- 生成批次 -->
    <SettingDrawer v-model:visible="taskVisible" :title="$t('codeWiki.workspace.taskDetails')"
      icon="task" width="760px" :min-width="320" hide-footer>
    <p class="cw-section-desc">{{ $t('codeWiki.workspace.drawerHint') }}</p>
    <p v-if="connectionLost" class="run-error" role="status">{{ $t('codeWiki.workspace.disconnected') }}</p>
    <section class="cw-section">
      <div class="cw-section-header">
        <div>
          <h3 class="cw-section-title">{{ $t('codeWiki.runs.title') }}</h3>
          <p class="cw-section-desc">{{ $t('codeWiki.runs.description') }}</p>
        </div>
        <div class="cw-section-actions">
          <template v-if="activeRun && displayedRun?.id === activeRun.id">
            <!-- staged: publish, or discard the candidates -->
            <template v-if="activeRun.status === 'staged'">
              <t-button size="small" theme="primary" :loading="actionPending" :disabled="!canEdit || actionPending || hasIncompletePages" @click="publishActiveRun">
                {{ $t('codeWiki.runs.publish') }}
              </t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit || actionPending"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.discard') }}</t-button>
            </template>
            <!-- paused: resume or cancel -->
            <template v-else-if="activeRun.status === 'paused'">
              <t-button v-if="impactPlan?.review_required" size="small" theme="primary" :disabled="!canEdit || actionPending" @click="approveImpact">
                {{ $t('codeWiki.workspace.impactReviewAction') }}
              </t-button>
              <t-button v-else size="small" theme="primary" :disabled="!canEdit || actionPending" @click="resumeActiveRun">
                {{ $t('codeWiki.runs.resume') }}
              </t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit || actionPending"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.cancel') }}</t-button>
            </template>
            <!-- in flight: pause or cancel -->
            <template v-else-if="activeRun.status !== 'publishing'">
              <t-button variant="outline" size="small" :disabled="!canEdit || actionPending || activeRun.pause_requested"
                @click="pauseActiveRun">{{ activeRun.pause_requested ? $t('codeWiki.workspace.pausing') : $t('codeWiki.runs.pause') }}</t-button>
              <t-button variant="outline" size="small" :disabled="!canEdit || actionPending"
                @click="cancelActiveRun">{{ $t('codeWiki.runs.cancel') }}</t-button>
            </template>
          </template>
          <t-button v-if="!activeRun" size="small" theme="primary" :loading="actionPending" :disabled="!canEdit || !readyRepositoryCount || anySyncing || actionPending"
            @click="createRun">
            {{ $t('codeWiki.runs.create') }}
          </t-button>
        </div>
      </div>

      <!-- 活动批次进度：阶段文字而非百分比 -->
      <div v-if="displayedRun" :class="['run-progress', displayedRun.status]">
        <div class="run-progress-main">
          <t-tag size="small" variant="light" :theme="runStatusTheme(displayedRun.status)">
            {{ runStatusLabel(displayedRun.status) }}
          </t-tag>
          <span class="run-phase" role="status" :aria-busy="isWorking">
            <t-loading v-if="isWorking" size="14px" />
            {{ displayedRun.phase || runStatusLabel(displayedRun.status) }}
          </span>
          <span v-if="displayedRun.total_pages" class="run-pages">
            {{ displayedRun.completed_pages }}/{{ displayedRun.total_pages }}
          </span>
          <span v-if="displayedRun.status === 'staged' && stagedConflictCount > 0" class="run-conflict-hint">
            {{ $t('codeWiki.runs.conflicts', { count: stagedConflictCount }) }}
          </span>
        </div>
        <p v-if="hasIncompletePages && displayedRun.status === 'staged'" class="run-error" role="status">
          {{ $t('codeWiki.workspace.incomplete') }}
        </p>
        <p v-if="displayedRun.pause_requested && ['pending', 'preparing', 'analyzing', 'planning', 'generating', 'validating'].includes(displayedRun.status)" role="status">
          {{ $t('codeWiki.runs.pauseRequested') }}
        </p>
        <div v-if="displayedRun.status === 'staged'" class="run-progress-actions">
          <t-radio-group v-model="publishConflictPolicy" variant="default-radio" size="small">
            <t-radio value="abort">{{ $t('codeWiki.runs.conflictAbort') }}</t-radio>
            <t-radio value="keep">{{ $t('codeWiki.runs.conflictKeep') }}</t-radio>
          </t-radio-group>
        </div>
        <p v-if="displayedRun.error_message" class="run-error">{{ displayedRun.error_message }}</p>
        <p v-if="displayedRun.updated_at" class="cw-section-desc">
          {{ $t('codeWiki.workspace.lastActivity') }}: {{ new Date(displayedRun.updated_at).toLocaleString() }}
        </p>
        <p v-if="workerHeartbeatAt && isWorking" class="cw-section-desc" role="status">
          {{ $t('codeWiki.workspace.workerHeartbeat') }}: {{ new Date(workerHeartbeatAt).toLocaleString() }}
          <span v-if="heartbeatStale" class="run-error"> · {{ $t('codeWiki.workspace.heartbeatStale') }}</span>
        </p>
        <p v-if="retryEvent && isWorking" class="cw-section-desc" role="status">
          {{ retryEvent.message }} · {{ $t('codeWiki.workspace.retryAt') }}: {{ new Date(retryEvent.retry_at!).toLocaleString() }}
        </p>
        <p v-if="latestActivity" class="run-current-activity" role="status" aria-live="polite">
          <t-loading v-if="activityRunning" size="14px" />
          <t-icon v-else :name="latestActivity.kind === 'operation_failed' ? 'error-circle' : 'time'" size="14px" />
          <span>{{ latestActivity.message }}</span>
        </p>
        <details v-if="runEvents.length && loadedRunId === displayedRun.id" class="run-manifest" :open="isWorking">
          <summary>{{ $t('codeWiki.workspace.activityHistory') }}</summary>
          <ol class="run-activity-list">
            <li v-for="event in runEvents" :key="event.id" :class="{ 'run-activity-failed': event.kind === 'operation_failed' }">
              <t-loading v-if="activityRunning && event.id === latestActivity?.id" size="12px" />
              <t-icon v-else :name="event.kind === 'operation_completed' ? 'check-circle' : event.kind === 'operation_failed' ? 'error-circle' : 'time'" size="12px" />
              <time>{{ new Date(event.created_at).toLocaleTimeString() }}</time>
              <span>{{ event.message }}</span>
            </li>
          </ol>
          <t-button v-if="hasOlderEvents" variant="text" size="small" :loading="loadingOlderEvents" @click="loadOlderEvents">
            {{ $t('codeWiki.workspace.olderEvents') }}
          </t-button>
        </details>
        <details v-if="sourceChanges.length" class="run-manifest">
          <summary>{{ $t('codeWiki.workspace.sourceChanges', { count: sourceChanges.length }) }}</summary>
          <p class="cw-section-desc">{{ $t('codeWiki.workspace.sourceChangesHint') }}</p>
          <p v-for="change in sourceChanges" :key="`${change.repository_id}:${change.path}`">
            {{ runManifest.find(repo => repo.repository_id === change.repository_id)?.display_name || change.repository_id }}
            · {{ change.kind }} · {{ change.path }}
          </p>
        </details>
        <section v-if="impactPlan?.review_required" class="candidate-detail">
          <h4>{{ $t('codeWiki.workspace.impactReviewTitle') }}</h4>
          <p class="cw-section-desc">{{ $t('codeWiki.workspace.impactReviewHint') }}</p>
          <fieldset v-for="page in runPages" :key="page.page_key" class="impact-page">
            <legend>{{ page.title }}</legend>
            <p v-if="impactPlan.impact?.[page.page_key]" class="cw-section-desc">
              {{ $t(`codeWiki.workspace.impact_${impactPlan.impact[page.page_key].kind}`) }}
            </p>
            <p v-for="source in impactPlan.impact?.[page.page_key]?.changed_sources || []" :key="source" class="cw-section-desc">{{ source }}</p>
            <t-checkbox-group v-model="selectedSections[page.page_key]" :disabled="!canEdit || actionPending">
              <t-checkbox v-for="section in impactPlan.sections?.[page.page_key] || []" :key="section.id" :value="section.id">
                {{ section.title }}
              </t-checkbox>
            </t-checkbox-group>
          </fieldset>
        </section>
        <details v-if="runManifest.length" class="run-manifest">
          <summary>{{ $t('codeWiki.workspace.frozenVersions') }}</summary>
          <p v-for="repo in runManifest" :key="repo.repository_id">
            {{ repo.display_name }} · {{ repo.ref || '—' }} · {{ repo.commit_sha || '—' }}
            <span v-if="repo.include_paths?.length"> · {{ repo.include_paths.join(', ') }}</span>
            <span v-if="repo.exclude_patterns?.length"> · − {{ repo.exclude_patterns.join(', ') }}</span>
            <span v-if="repo.excluded" class="run-error">{{ repo.exclude_reason }}</span>
          </p>
        </details>
        <!-- Per-page progress: planned pages show as pending before generation
             reaches them; failures carry the concrete reason. -->
        <CodeWikiDocumentTree v-if="runPages.length" :key="displayedRun?.id" class="run-page-list" :nodes="runDocumentTree">
          <template #page="{ page: p }">
          <div class="run-page-item">
            <button type="button" class="run-page-name page-link" aria-haspopup="dialog"
              @click="openDocumentPreview(p.page_key)">{{ p.title || p.page_key }}</button>
            <t-tag v-if="p.generation_error" size="small" variant="light" theme="danger">
              {{ $t('codeWiki.runs.pageFailed') }}
            </t-tag>
            <t-tag v-else-if="p.status === 'pending' && ['retrieving', 'generating', 'validating'].includes(p.validation_status)"
              size="small" variant="light" theme="primary">
              <t-loading v-if="isWorking" size="12px" />
              {{ $t(`codeWiki.workspace.${p.validation_status}`) }}
            </t-tag>
            <t-tag v-else-if="p.status === 'pending'" size="small" variant="light" theme="default">
              {{ $t('codeWiki.runs.pagePending') }}
            </t-tag>
            <t-tag v-else-if="p.status === 'conflict' || p.status === 'skipped' || p.status === 'preserved'" size="small" variant="light" theme="warning">
              {{ $t(`codeWiki.workspace.${p.status === 'skipped' && impactPlan?.new_pages?.[p.page_key] ? 'notCreated' : p.status}`) }}
            </t-tag>
            <t-tag v-else size="small" variant="light" theme="success">
              {{ $t('codeWiki.runs.pageDone') }}
            </t-tag>
            <t-tooltip v-if="p.generation_error" :content="p.generation_error" placement="top">
              <span class="run-page-error">{{ p.generation_error }}</span>
            </t-tooltip>
          </div>
          </template>
        </CodeWikiDocumentTree>
      </div>

      <t-table v-if="runs.length" row-key="id" :data="runs" :columns="runColumns" size="small" :loading="runsLoading">
        <template #status="{ row }">
          <t-tag size="small" variant="light" :theme="runStatusTheme(row.status)">
            {{ runStatusLabel(row.status) }}
          </t-tag>
        </template>
        <template #run-actions="{ row }">
          <t-button variant="text" size="small" @click="openTask(row.id)">{{ $t('codeWiki.workspace.viewProgress') }}</t-button>
          <t-button v-if="row.status === 'failed' || row.status === 'canceled'" variant="text" size="small"
            theme="primary" :disabled="!!activeRun || !canEdit || actionPending" @click="retryRun(row)">
            {{ $t('codeWiki.runs.retry') }}
          </t-button>
        </template>
      </t-table>
    </section>
    </SettingDrawer>

    <CodeWikiDocumentPreview v-model:visible="documentPreviewVisible" v-model:page-key="selectedPageKey"
      :pages="runPages" :working="isWorking" :new-pages="impactPlan?.new_pages" />

    <t-dialog :visible="!!syncErrorRepository" :header="$t('codeWiki.repositories.errorDetails')"
      :footer="false" width="680px" @close="syncErrorRepository = null">
      <p>{{ syncErrorRepository?.display_name }} · {{ syncErrorRepository?.ref || $t('codeWiki.workspace.defaultBranch') }}</p>
      <p class="sync-error-detail">{{ syncErrorRepository?.error_message }}</p>
    </t-dialog>

    <!-- 添加仓库弹窗 -->
    <CodeWikiRepositoryDialog v-model:visible="repoDialogVisible" :knowledge-base-id="knowledgeBaseId"
      :data-sources="gitlabSources" :editing="editingRepository" @created="onRepositoryCreated"
      @open-kb-settings="repoDialogVisible = false; sourcesVisible = true" />

    <!-- Provider 配置弹窗 -->
    <CodeWikiProviderDialog v-model:visible="providerDialogVisible" :knowledge-base-id="knowledgeBaseId"
      :profile="profile" @saved="onProviderSaved" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  listCodeWikiRepositories, syncCodeWikiRepositories, deleteCodeWikiRepository,
  listCodeWikiRuns, createCodeWikiRun, cancelCodeWikiRun, pauseCodeWikiRun,
  resumeCodeWikiRun, retryCodeWikiRun, publishCodeWikiRun, getCodeWikiRun,
  getCodeWikiAnalysisProfile, testCodeWikiAnalysisProfile,
  listCodeWikiRunEvents,
  listCodeWikiSourceDirectory,
  listCodeWikiSourcePages,
  approveCodeWikiImpact,
  exportCodeWikiOKF,
} from '@/api/code-wiki'
import type {
  CodeWikiAnalysisProfile, CodeWikiCapabilityReport, CodeWikiRepositoryBinding, CodeWikiRun,
  CodeWikiRunPage, CodeWikiRunRepository,
  CodeWikiRunEvent, CodeWikiSourceChange,
  CodeWikiSourceDirectory, CodeWikiSourceEntry,
  CodeWikiSourcePage,
  CodeWikiImpactPlan,
} from '@/api/code-wiki'
import { listDataSources } from '@/api/datasource'
import CodeWikiRepositoryDialog from './CodeWikiRepositoryDialog.vue'
import CodeWikiDocumentPreview from './CodeWikiDocumentPreview.vue'
import CodeWikiDocumentTree from './CodeWikiDocumentTree.vue'
import { buildDocumentTree } from './documentTree'
import CodeWikiProviderDialog from './CodeWikiProviderDialog.vue'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import DataSourceSettings from '../settings/DataSourceSettings.vue'

const props = defineProps<{
  knowledgeBaseId: string
  canEdit: boolean
  showSources?: boolean
}>()

const exportingOKF = ref(false)
const downloadOKF = async () => {
  exportingOKF.value = true
  try {
    const blob = await exportCodeWikiOKF(props.knowledgeBaseId)
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = 'frontend-knowledge-okf.zip'
    document.body.appendChild(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch {
    MessagePlugin.error(t('codeWiki.template.exportFailed'))
  } finally {
    exportingOKF.value = false
  }
}

const emit = defineEmits<{
  (e: 'open-kb-settings'): void
  (e: 'published'): void
  (e: 'open-wiki-page', slug: string): void
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
const sourcesVisible = ref(false)
const taskVisible = ref(false)
const selectedRunId = ref('')
const selectedPageKey = ref('')
const documentPreviewVisible = ref(false)
const openDocumentPreview = (pageKey: string) => {
  selectedPageKey.value = pageKey
  documentPreviewVisible.value = true
}
const editingRepository = ref<CodeWikiRepositoryBinding | null>(null)
const connectionLost = ref(false)
const actionPending = ref(false)
const loadedRunId = ref('')
const runEvents = ref<CodeWikiRunEvent[]>([])
const sourceChanges = ref<CodeWikiSourceChange[]>([])
const workerHeartbeatAt = ref<string | null>(null)
const observedAt = ref(Date.now())
const hasOlderEvents = ref(false)
const loadingOlderEvents = ref(false)
const impactPlan = ref<CodeWikiImpactPlan | null>(null)
const selectedSections = ref<Record<string, string[]>>({})
const sourceTreeVisible = ref(false)
const sourceTreeLoading = ref(false)
const sourceTreeError = ref('')
const sourceRepository = ref<CodeWikiRepositoryBinding | null>(null)
const sourceDirectory = ref<CodeWikiSourceDirectory | null>(null)
const sourcePrefix = ref('')
const sourceSnapshotId = ref<string | undefined>()
const selectedSourceFile = ref<CodeWikiSourceEntry | null>(null)
const publishedSourcePages = ref<CodeWikiSourcePage[]>([])
const sourcePagesCursor = ref('')
const sourcePagesLoading = ref(false)
const sourcePagesError = ref('')
let sourcePagesSeq = 0
const selectSourceFile = (entry: CodeWikiSourceEntry) => {
  selectedSourceFile.value = entry
  publishedSourcePages.value = []
  sourcePagesCursor.value = ''
  loadSourcePages()
}
const loadSourcePages = async (append = false) => {
  const repo = sourceRepository.value, directory = sourceDirectory.value, file = selectedSourceFile.value
  if (!repo || !directory || !file) return
  const seq = ++sourcePagesSeq
  sourcePagesLoading.value = true
  sourcePagesError.value = ''
  try {
    const res: any = await listCodeWikiSourcePages(props.knowledgeBaseId, repo.id, {snapshot_id: directory.snapshot_id, path: file.path, after: append ? sourcePagesCursor.value : undefined})
    if (seq !== sourcePagesSeq || sourceRepository.value?.id !== repo.id || selectedSourceFile.value?.path !== file.path) return
    const data = res?.data ?? res
    publishedSourcePages.value = [...(append ? publishedSourcePages.value : []), ...(data.pages ?? [])]
    sourcePagesCursor.value = data.next_cursor ?? ''
  } catch (err: any) {
    if (seq === sourcePagesSeq) sourcePagesError.value = err?.message || t('codeWiki.loadFailed')
  } finally { if (seq === sourcePagesSeq) sourcePagesLoading.value = false }
}
let sourceTreeSeq = 0
const sourceRelatedPages = computed(() => runPages.value.filter(page => page.evidence?.some(evidence =>
  evidence.repository_id === sourceRepository.value?.id && evidence.snapshot_id === sourceDirectory.value?.snapshot_id && evidence.path === selectedSourceFile.value?.path)))
const selectedSourceFileUrl = computed(() => {
  const web = sourceRepository.value?.web_url
  const commit = sourceDirectory.value?.commit_sha
  if (!web || !/^https?:\/\//i.test(web) || !commit || !selectedSourceFile.value) return ''
  return `${web.replace(/\/$/, '')}/-/blob/${encodeURIComponent(commit)}/${selectedSourceFile.value.path.split('/').map(encodeURIComponent).join('/')}`
})
const openSourceRepository = (repo: CodeWikiRepositoryBinding) => {
  ++sourcePagesSeq
  selectedSourceFile.value = null
  publishedSourcePages.value = []
  sourcePagesCursor.value = ''
  sourcePagesLoading.value = false
  sourceRepository.value = repo
  sourceSnapshotId.value = repo.last_snapshot_id || undefined
  sourceDirectory.value = null
  sourceTreeVisible.value = true
  loadSourceDirectory('')
}
const loadSourceDirectory = async (prefix: string, append = false) => {
  const repo = sourceRepository.value
  if (!repo) return
  const seq = ++sourceTreeSeq
  const snapshot = sourceSnapshotId.value
  const after = append ? sourceDirectory.value?.next_cursor : undefined
  if (!append) { sourceDirectory.value = null; selectedSourceFile.value = null; ++sourcePagesSeq; sourcePagesLoading.value = false }
  sourcePrefix.value = prefix
  sourceTreeLoading.value = true
  sourceTreeError.value = ''
  try {
    const res: any = await listCodeWikiSourceDirectory(props.knowledgeBaseId, repo.id, { snapshot_id: snapshot, prefix, after })
    if (seq !== sourceTreeSeq || sourceRepository.value?.id !== repo.id) return
    const directory: CodeWikiSourceDirectory = res?.data ?? res
    sourceSnapshotId.value = directory.snapshot_id
    sourceDirectory.value = { ...directory, entries: [...(append ? sourceDirectory.value?.entries ?? [] : []), ...directory.entries] }
  } catch (err: any) {
    if (seq === sourceTreeSeq) sourceTreeError.value = err?.message || t('codeWiki.loadFailed')
  } finally { if (seq === sourceTreeSeq) sourceTreeLoading.value = false }
}
const publishConflictPolicy = ref<'abort' | 'keep'>('abort')

let pollTimer: ReturnType<typeof setInterval> | null = null

const capabilityReport = computed<CodeWikiCapabilityReport | null>(() => profile.value?.capability_check ?? null)
const activeRun = computed<CodeWikiRun | null>(() => {
  const terminal = ['published', 'superseded', 'failed', 'canceled']
  return runs.value.find((run) => !terminal.includes(run.status)) ?? null
})
const displayedRun = computed(() => runs.value.find(run => run.id === selectedRunId.value) ?? activeRun.value ?? runs.value[0] ?? null)
const isExecuting = (status: string) => ['pending', 'preparing', 'analyzing', 'planning', 'generating', 'validating', 'publishing'].includes(status)
const isWorking = computed(() => isExecuting(displayedRun.value?.status ?? ''))
const latestActivity = computed(() => loadedRunId.value === displayedRun.value?.id ? runEvents.value[0] : undefined)
const activityRunning = computed(() => isWorking.value && !connectionLost.value && !heartbeatStale.value && latestActivity.value?.kind === 'operation_started')
const heartbeatStale = computed(() => workerHeartbeatAt.value && observedAt.value - Date.parse(workerHeartbeatAt.value) > 45000)
const retryEvent = computed(() => runEvents.value[0]?.kind === 'retry' && runEvents.value[0].retry_at ? runEvents.value[0] : null)
const hasIncompletePages = computed(() => loadedRunId.value !== displayedRun.value?.id || !runPages.value.length || runPages.value.length !== displayedRun.value?.total_pages ||
  (displayedRun.value?.failed_pages ?? 0) > 0 || runPages.value.some(page =>
    !!page.generation_error || page.status === 'pending' || page.validation_status !== 'passed'))
const openTask = (id?: string) => {
  selectedRunId.value = id ?? activeRun.value?.id ?? runs.value[0]?.id ?? ''
  taskVisible.value = true
  selectedPageKey.value = ''
  loadRunDetail()
}
const editRepository = (repo?: CodeWikiRepositoryBinding) => {
  editingRepository.value = repo ?? null
  repoDialogVisible.value = true
}
const onSourcesVisibility = (visible: boolean) => {
  if (!visible) loadGitlabSources()
}
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
    connectionLost.value = false
  } catch {
    connectionLost.value = true
  } finally {
    runsLoading.value = false
  }
}

// Per-page progress for the active run (planned pages appear as pending
// before generation reaches them; errors carry the per-page reason).
const runPages = ref<CodeWikiRunPage[]>([])
const runDocumentTree = computed(() => buildDocumentTree(runPages.value))
const runManifest = ref<CodeWikiRunRepository[]>([])
let runDetailSeq = 0
const loadRunDetail = async () => {
  const seq = ++runDetailSeq
  const run = displayedRun.value
  if (!run) {
    runPages.value = []
    runManifest.value = []
    return
  }
  try {
    const res: any = await getCodeWikiRun(props.knowledgeBaseId, run.id)
    if (seq !== runDetailSeq || displayedRun.value?.id !== run.id) return
    const detail = res?.data ?? res
    runPages.value = detail?.pages ?? []
    runManifest.value = detail?.manifest ?? []
    const freshEvents: CodeWikiRunEvent[] = detail?.events ?? []
    const previous = loadedRunId.value === run.id ? runEvents.value : []
    // Tool/file operations can produce more than one event page per poll.
    // Fill the interval before merging so the history has no hidden gaps.
    let eventPage = freshEvents
    while (previous[0] && eventPage.length === 50 && eventPage.at(-1)!.id > previous[0].id) {
      const more: any = await listCodeWikiRunEvents(props.knowledgeBaseId, run.id, eventPage.at(-1)!.id)
      if (seq !== runDetailSeq || displayedRun.value?.id !== run.id) return
      const nextPage: CodeWikiRunEvent[] = more?.data ?? more ?? []
      if (nextPage.length && nextPage[0].id >= eventPage.at(-1)!.id) break
      eventPage = nextPage
      freshEvents.push(...eventPage)
    }
    if (loadedRunId.value !== run.id) hasOlderEvents.value = eventPage.length === 50
    const currentEvents = loadedRunId.value === run.id ? runEvents.value : []
    runEvents.value = [...new Map([...currentEvents, ...freshEvents].map(event => [event.id, event])).values()].sort((a, b) => b.id - a.id)
    sourceChanges.value = detail?.source_changes ?? []
    workerHeartbeatAt.value = detail?.worker_heartbeat_at ?? null
    const incomingPlan: CodeWikiImpactPlan | null = detail?.impact_plan ?? null
    if (impactPlan.value?.plan_token !== incomingPlan?.plan_token) {
      selectedSections.value = Object.fromEntries(runPages.value.map(page => [page.page_key, []]))
    }
    impactPlan.value = incomingPlan
    observedAt.value = Date.now()
    loadedRunId.value = run.id
    if (detail?.run) runs.value = runs.value.map(item => item.id === run.id ? detail.run : item)
    connectionLost.value = false
  } catch {
    connectionLost.value = true
  }
}

const loadOlderEvents = async () => {
  const runId = loadedRunId.value
  const before = runEvents.value.at(-1)?.id
  if (!runId || !before || loadingOlderEvents.value) return
  loadingOlderEvents.value = true
  try {
    const res: any = await listCodeWikiRunEvents(props.knowledgeBaseId, runId, before)
    if (loadedRunId.value !== runId || displayedRun.value?.id !== runId) return
    const older: CodeWikiRunEvent[] = res?.data ?? res ?? []
    runEvents.value = [...new Map([...runEvents.value, ...older].map(event => [event.id, event])).values()].sort((a, b) => b.id - a.id)
    hasOlderEvents.value = older.length === 50
  } catch { MessagePlugin.error(t('codeWiki.loadFailed')) }
  finally { loadingOlderEvents.value = false }
}

const approveImpact = async () => {
  const run = displayedRun.value
  const plan = impactPlan.value
  if (!run || !plan?.review_required || actionPending.value) return
  actionPending.value = true
  try {
    await approveCodeWikiImpact(props.knowledgeBaseId, run.id, plan.plan_token, selectedSections.value)
    await loadRuns()
    await loadRunDetail()
  } catch (err: any) { MessagePlugin.error(err?.message || t('codeWiki.loadFailed')) }
  finally { actionPending.value = false }
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
  await loadRunDetail()
}

watch(() => displayedRun.value?.id, () => {
  documentPreviewVisible.value = false
  loadedRunId.value = ''
  runEvents.value = []
  sourceChanges.value = []
  workerHeartbeatAt.value = null
  impactPlan.value = null
  selectedSections.value = {}
  runPages.value = []
  runManifest.value = []
  selectedPageKey.value = ''
  loadRunDetail()
})

let polling = false
onMounted(() => {
  refreshAll()
  // Discover work started in another tab and retain observation while the
  // source pane is hidden; never overlap polling batches.
  pollTimer = setInterval(async () => {
    observedAt.value = Date.now()
    const syncing = repositories.value.some((r) => r.status === 'syncing')
    if (!polling) {
      polling = true
      try {
        if (syncing || sourcesVisible.value) await loadRepositories()
        await loadRuns()
        if (activeRun.value || taskVisible.value) await loadRunDetail()
      } finally {
        polling = false
      }
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

const syncErrorRepository = ref<CodeWikiRepositoryBinding | null>(null)

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
  if (actionPending.value) return
  actionPending.value = true
  try {
    const res: any = await createCodeWikiRun(props.knowledgeBaseId)
    const created = res?.data ?? res
    MessagePlugin.success(t(created?.no_changes ? 'codeWiki.workspace.noChanges' : 'codeWiki.runs.created'))
    await loadRuns()
    openTask(created?.id)
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    actionPending.value = false
  }
}

const performRunAction = async (action: (kbId: string, runId: string) => Promise<any>, message: string) => {
  if (!activeRun.value || actionPending.value) return
  actionPending.value = true
  try {
    await action(props.knowledgeBaseId, activeRun.value.id)
    MessagePlugin.success(t(message))
    await loadRuns()
    await loadRunDetail()
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    actionPending.value = false
  }
}

const cancelActiveRun = () => performRunAction(cancelCodeWikiRun, 'codeWiki.runs.cancelRequested')
const pauseActiveRun = () => performRunAction(pauseCodeWikiRun, 'codeWiki.runs.pauseRequested')
const resumeActiveRun = () => performRunAction(resumeCodeWikiRun, 'codeWiki.runs.resumed')

const retryRun = async (row: CodeWikiRun) => {
  if (actionPending.value) return
  actionPending.value = true
  try {
    const res: any = await retryCodeWikiRun(props.knowledgeBaseId, row.id)
    const created = res?.data ?? res
    MessagePlugin.success(t(created?.no_changes ? 'codeWiki.workspace.noChanges' : 'codeWiki.runs.created'))
    await loadRuns()
    openTask(created?.id)
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    actionPending.value = false
  }
}

const publishActiveRun = async () => {
  if (!activeRun.value || actionPending.value || hasIncompletePages.value) return
  actionPending.value = true
  try {
    await publishCodeWikiRun(props.knowledgeBaseId, activeRun.value.id, publishConflictPolicy.value)
    stagedConflicts.value = 0
    MessagePlugin.success(t('codeWiki.runs.published'))
    await loadRuns()
    await loadRunDetail()
    emit('published')
  } catch (err: any) {
    const message: string = err?.message ?? ''
    if (message.includes('publish paused') || message.includes('conflict')) {
      stagedConflicts.value = 1
      MessagePlugin.warning(message || t('codeWiki.runs.conflictHint'))
    } else {
      MessagePlugin.error(message || t('codeWiki.loadFailed'))
    }
  } finally {
    actionPending.value = false
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
  { colKey: 'actions', title: t('common.actions'), width: 210 },
])

const runColumns = computed(() => [
  { colKey: 'created_at', title: t('codeWiki.runs.columnTime'), width: 170,
    cell: (_h: any, { row }: any) => new Date(row.created_at).toLocaleString() },
  { colKey: 'status', title: t('codeWiki.runs.columnStatus'), width: 110 },
  { colKey: 'completed_pages', title: t('codeWiki.runs.columnPages'), width: 90,
    cell: (_h: any, { row }: any) => `${row.completed_pages}/${row.total_pages}` },
  { colKey: 'model_calls', title: t('codeWiki.runs.columnModelCalls'), width: 100 },
  { colKey: 'run-actions', title: t('common.actions'), width: 180 },
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

.workspace-toolbar, .source-card, .task-card {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.workspace-toolbar { align-items: center; padding: 0 8px; }
.source-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(240px, 100%), 1fr)); gap: 12px; }
.source-card, .task-card {
  color: var(--td-text-color-primary);
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-border);
  border-radius: var(--td-radius-medium);
  padding: 16px;
  text-align: left;
  cursor: pointer;
  overflow-wrap: anywhere;
}
.source-card { flex-direction: column; font-size: 13px; }
.source-card-main { display: flex; flex-direction: column; gap: 8px; width: 100%; padding: 0; border: 0; background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; overflow-wrap: anywhere; }
.task-card { width: 100%; justify-content: space-between; margin-top: 16px; }
.source-card:hover, .task-card:hover { border-color: var(--td-brand-color); }
.page-link { background: none; border: 0; padding: 4px 0; color: var(--td-brand-color); cursor: pointer; text-align: left; }
.candidate-detail { border-top: 1px solid var(--td-component-border); margin-top: 12px; overflow-wrap: anywhere; }
.run-manifest { font-size: 12px; overflow-wrap: anywhere; margin-top: 12px; }
.source-breadcrumb { display: flex; flex-wrap: wrap; gap: 4px; }
.source-file-list { list-style: none; padding: 0; }
.source-file-list li { display: flex; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--td-component-border); }
.source-file-list .page-link { overflow-wrap: anywhere; }
.impact-page { border: 1px solid var(--td-component-border); border-radius: 6px; margin: 12px 0; padding: 12px; }
.impact-page :deep(.t-checkbox-group) { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; }
@media (max-width: 600px) {
  .cw-section { padding: 12px; }
  .cw-section-header { flex-direction: column; }
  .cw-section-actions { flex-wrap: wrap; }
  .run-page-name { max-width: 65%; }
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
  display: block;
  max-width: 100%;
  padding: 0;
  border: 0;
  background: none;
  color: var(--td-error-color);
  font-size: 12px;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.sync-error-detail {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 60vh;
  overflow-y: auto;
  color: var(--td-error-color);
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
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--td-text-color-primary);
}

.run-current-activity {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 10px 0;
  font-size: 13px;
  overflow-wrap: anywhere;
  color: var(--td-text-color-primary);
}

.run-activity-list {
  max-height: 260px;
  overflow-y: auto;
  list-style: none;
  padding: 0;

  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 0;
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  time {
    flex-shrink: 0;
    color: var(--td-text-color-placeholder);
    font-variant-numeric: tabular-nums;
  }

  .t-icon, :deep(.t-loading) { flex-shrink: 0; }
  .run-activity-failed { color: var(--td-warning-color); }
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
  padding: 6px 4px;
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
