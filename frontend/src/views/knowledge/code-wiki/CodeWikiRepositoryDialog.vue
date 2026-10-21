<template>
  <t-dialog v-model:visible="dialogVisible" :header="$t('codeWiki.repoDialog.title')" :footer="false" width="560px"
    destroy-on-close>
    <div class="repo-form">
      <!-- GitLab 数据源（连接）：code_wiki KB 的连接在设置里配置 -->
      <div class="form-item">
        <label class="form-label required">{{ $t('codeWiki.repoDialog.source') }}</label>
        <t-select v-model="form.sourceId" :placeholder="$t('codeWiki.repoDialog.sourcePlaceholder')" clearable>
          <t-option v-for="ds in dataSources" :key="ds.id" :value="ds.id" :label="ds.name" />
        </t-select>
        <p v-if="!dataSources.length" class="form-tip">
          {{ $t('codeWiki.repoDialog.noSource') }}
          <a class="form-tip-link" @click="$emit('open-kb-settings')">{{ $t('codeWiki.repoDialog.goSettings') }}</a>
        </p>
      </div>

      <div class="form-item">
        <label class="form-label required">{{ $t('codeWiki.repoDialog.project') }}</label>
        <t-input v-model="form.projectId" :placeholder="$t('codeWiki.repoDialog.projectPlaceholder')" />
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
import { createCodeWikiRepository } from '@/api/code-wiki'

const props = defineProps<{
  visible: boolean
  knowledgeBaseId: string
  dataSources: any[]
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
    form.sourceId = props.dataSources[0]?.id ?? ''
    form.projectId = ''
    form.role = 'backend'
    form.ref = ''
    form.includePaths = ''
    form.excludePatterns = ''
  }
})

const canSubmit = computed(() => form.sourceId && form.projectId.trim())

// 逗号/换行分隔 → 数组；空行过滤。
const parseList = (raw: string): string[] =>
  raw.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)

const submit = async () => {
  submitting.value = true
  try {
    await createCodeWikiRepository(props.knowledgeBaseId, {
      source_id: form.sourceId,
      project_id: form.projectId.trim(),
      role: form.role,
      ref: form.ref.trim(),
      include_paths: parseList(form.includePaths),
      exclude_patterns: parseList(form.excludePatterns),
    })
    MessagePlugin.success(t('codeWiki.repoDialog.created'))
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
