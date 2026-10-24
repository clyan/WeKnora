<template>
  <t-dialog v-model:visible="dialogVisible" :header="editing ? $t('codeWiki.workspace.editRepository') : $t('codeWiki.repoDialog.title')" :footer="false" width="min(560px, 100vw)"
    :z-index="2700" destroy-on-close>
    <div class="repo-form">
      <!-- GitLab 数据源（连接）：code_wiki KB 的连接在设置里配置 -->
      <div class="form-item">
        <label class="form-label required">{{ $t('codeWiki.repoDialog.source') }}</label>
        <t-select v-model="form.sourceId" :disabled="!!editing" :placeholder="$t('codeWiki.repoDialog.sourcePlaceholder')" clearable>
          <t-option v-for="ds in dataSources" :key="ds.id" :value="ds.id" :label="ds.name" />
        </t-select>
        <p v-if="!dataSources.length" class="form-tip">
          {{ $t('codeWiki.repoDialog.noSource') }}
          <button type="button" class="form-tip-link" @click="$emit('open-kb-settings')">{{ $t('codeWiki.repoDialog.goSettings') }}</button>
        </p>
      </div>

      <div class="form-item">
        <label class="form-label required">{{ $t('codeWiki.repoDialog.project') }}</label>
        <t-input v-model="form.projectId" :disabled="!!editing" :placeholder="$t('codeWiki.repoDialog.projectPlaceholder')" />
        <p class="form-tip">{{ $t('codeWiki.repoDialog.projectTip') }}</p>
      </div>

      <div class="form-row">
        <div class="form-item">
          <label class="form-label">{{ $t('codeWiki.repoDialog.role') }}</label>
          <t-select v-model="form.role">
            <t-option value="frontend" :label="$t('codeWiki.roles.frontend')" />
            <t-option value="backend" :label="$t('codeWiki.roles.backend')" />
            <t-option value="service" :label="$t('codeWiki.roles.service')" />
            <t-option value="library" :label="$t('codeWiki.roles.library')" />
            <t-option value="other" :label="$t('codeWiki.roles.other')" />
          </t-select>
        </div>
        <div class="form-item">
          <label class="form-label">{{ $t('codeWiki.repoDialog.ref') }}</label>
          <t-input v-model="form.ref" :placeholder="$t('codeWiki.repoDialog.refPlaceholder')" />
        </div>
      </div>

      <p v-if="editing" class="form-tip">{{ $t('codeWiki.workspace.branchEditHint') }}</p>
      <div class="form-item">
        <label class="form-label">{{ $t('codeWiki.repoDialog.includePaths') }}</label>
        <t-textarea v-model="form.includePaths" :placeholder="$t('codeWiki.repoDialog.includePathsPlaceholder')"
          :autosize="{ minRows: 2, maxRows: 4 }" />
      </div>

      <div class="form-item">
        <label class="form-label">{{ $t('codeWiki.repoDialog.excludePatterns') }}</label>
        <t-textarea v-model="form.excludePatterns"
          :placeholder="$t('codeWiki.repoDialog.excludePatternsPlaceholder')" :autosize="{ minRows: 2, maxRows: 4 }" />
      </div>

      <div class="form-actions">
        <t-button variant="outline" @click="dialogVisible = false">{{ $t('common.cancel') }}</t-button>
        <t-button theme="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
          {{ $t('common.confirm') }}
        </t-button>
      </div>
    </div>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { createCodeWikiRepository, updateCodeWikiRepository, type CodeWikiRepositoryBinding } from '@/api/code-wiki'

const props = defineProps<{
  visible: boolean
  knowledgeBaseId: string
  dataSources: any[]
  editing?: CodeWikiRepositoryBinding | null
}>()

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'created'): void
  (e: 'open-kb-settings'): void
}>()

const { t } = useI18n()
const submitting = ref(false)

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const form = reactive({
  sourceId: '',
  projectId: '',
  role: 'backend',
  ref: '',
  includePaths: '',
  excludePatterns: '',
})

watch(dialogVisible, (visible) => {
  if (visible) {
    form.sourceId = props.editing?.source_id ?? props.dataSources[0]?.id ?? ''
    form.projectId = props.editing?.project_id ?? ''
    form.role = props.editing?.role ?? 'backend'
    form.ref = props.editing?.ref ?? ''
    form.includePaths = props.editing?.include_paths?.join('\n') ?? ''
    form.excludePatterns = props.editing?.exclude_patterns?.join('\n') ?? ''
  }
})

const canSubmit = computed(() => form.sourceId && form.projectId.trim())

// 逗号/换行分隔 → 数组；空行过滤。
const parseList = (raw: string): string[] =>
  raw.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)

const submit = async () => {
  if (submitting.value || !canSubmit.value) return
  submitting.value = true
  try {
    const payload = {
      source_id: form.sourceId,
      project_id: form.projectId.trim(),
      role: form.role,
      ref: form.ref.trim(),
      include_paths: parseList(form.includePaths),
      exclude_patterns: parseList(form.excludePatterns),
      ...(props.editing ? { display_name: props.editing.display_name, enabled: props.editing.enabled } : {}),
    }
    if (props.editing) {
      await updateCodeWikiRepository(props.knowledgeBaseId, props.editing.id, payload)
    } else {
      await createCodeWikiRepository(props.knowledgeBaseId, payload)
    }
    MessagePlugin.success(t(props.editing ? 'common.saveSuccess' : 'codeWiki.repoDialog.created'))
    emit('created')
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.repo-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.form-label {
  font-size: 13px;
  color: var(--td-text-color-primary);
}

.form-label.required::after {
  content: ' *';
  color: var(--td-error-color);
}

.form-tip {
  margin: 0;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.form-tip-link {
  border: 0;
  background: none;
  padding: 0;
  color: var(--td-brand-color);
  cursor: pointer;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}
</style>
