<template>
  <t-dialog v-model:visible="dialogVisible" :header="$t('codeWiki.providerDialog.title')" :footer="false"
    width="min(560px, 100vw)" :z-index="2700" destroy-on-close>
    <div class="provider-form">
      <div class="form-item">
        <label class="form-label required">{{ $t('codeWiki.providerDialog.kind') }}</label>
        <t-radio-group v-model="form.providerKind">
          <t-radio value="builtin_codebase">{{ $t('codeWiki.providerDialog.builtin') }}</t-radio>
          <t-radio value="custom_mcp">{{ $t('codeWiki.providerDialog.customMcp') }}</t-radio>
        </t-radio-group>
        <p class="form-tip">{{ $t('codeWiki.providerDialog.kindTip') }}</p>
      </div>

      <template v-if="form.providerKind === 'custom_mcp'">
        <div class="form-item">
          <label class="form-label required">{{ $t('codeWiki.providerDialog.mcpService') }}</label>
          <t-select v-model="form.mcpServiceId" :placeholder="$t('codeWiki.providerDialog.mcpServicePlaceholder')"
            :loading="loadingServices" clearable>
            <t-option v-for="svc in mcpServices" :key="svc.id" :value="svc.id"
              :label="svc.name + (svc.enabled ? '' : '（' + $t('codeWiki.providerDialog.disabledService') + '）')" />
          </t-select>
          <p class="form-tip">
            {{ $t('codeWiki.providerDialog.mcpServiceTip') }}
            <a class="form-tip-link" @click="goToMcpSettings">{{ $t('codeWiki.providerDialog.goMcpSettings') }}</a>
          </p>
        </div>

        <div class="form-item">
          <label class="form-label">{{ $t('codeWiki.providerDialog.delivery') }}</label>
          <t-select v-model="form.snapshotDelivery">
            <t-option value="shared_dir" :label="$t('codeWiki.providerDialog.deliverySharedDir')" />
            <t-option value="import" :label="$t('codeWiki.providerDialog.deliveryImport')" />
            <t-option value="external_index" :label="$t('codeWiki.providerDialog.deliveryExternal')" />
          </t-select>
        </div>

        <div v-if="form.snapshotDelivery === 'shared_dir'" class="form-item">
          <label class="form-label required">{{ $t('codeWiki.providerDialog.sharedRoot') }}</label>
          <t-input v-model="form.sharedRoot" :placeholder="$t('codeWiki.providerDialog.sharedRootPlaceholder')" />
          <p class="form-tip">{{ $t('codeWiki.providerDialog.sharedRootTip') }}</p>
        </div>
      </template>

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
import { useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { updateCodeWikiAnalysisProfile } from '@/api/code-wiki'
import type { CodeWikiAnalysisProfile } from '@/api/code-wiki'
import { listMCPServices } from '@/api/mcp-service'

const props = defineProps<{
  visible: boolean
  knowledgeBaseId: string
  profile: CodeWikiAnalysisProfile | null
}>()

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'saved', profile: CodeWikiAnalysisProfile): void
}>()

const { t } = useI18n()
const router = useRouter()
const submitting = ref(false)
const loadingServices = ref(false)
const mcpServices = ref<any[]>([])

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const form = reactive({
  providerKind: 'builtin_codebase' as 'builtin_codebase' | 'custom_mcp',
  mcpServiceId: '',
  snapshotDelivery: 'shared_dir' as 'local' | 'shared_dir' | 'import' | 'external_index',
  sharedRoot: '',
})

const loadServices = async () => {
  loadingServices.value = true
  try {
    // listMCPServices returns the bare array (no {data} envelope).
    const res: any = await listMCPServices()
    mcpServices.value = Array.isArray(res) ? res : (res?.data ?? [])
  } catch {
    mcpServices.value = []
  } finally {
    loadingServices.value = false
  }
}

watch(dialogVisible, (visible) => {
  if (visible) {
    form.providerKind = props.profile?.provider_kind === 'custom_mcp' ? 'custom_mcp' : 'builtin_codebase'
    form.mcpServiceId = props.profile?.mcp_service_id ?? ''
    form.snapshotDelivery = props.profile?.snapshot_delivery === 'local'
      ? 'shared_dir'
      : (props.profile?.snapshot_delivery ?? 'shared_dir')
    form.sharedRoot = props.profile?.shared_root ?? ''
    if (form.providerKind === 'custom_mcp') {
      loadServices()
    }
  }
})

watch(() => form.providerKind, (kind) => {
  if (kind === 'custom_mcp' && !mcpServices.value.length) {
    loadServices()
  }
})

const canSubmit = computed(() =>
  form.providerKind === 'builtin_codebase' ||
  (form.mcpServiceId && (form.snapshotDelivery !== 'shared_dir' || form.sharedRoot.trim())),
)

const goToMcpSettings = () => {
  dialogVisible.value = false
  router.push({ name: 'mcp-settings' })
}

const submit = async () => {
  submitting.value = true
  try {
    const res: any = await updateCodeWikiAnalysisProfile(props.knowledgeBaseId, {
      provider_kind: form.providerKind,
      mcp_service_id: form.providerKind === 'custom_mcp' ? form.mcpServiceId : '',
      snapshot_delivery: form.providerKind === 'builtin_codebase' ? 'local' : form.snapshotDelivery,
      shared_root: form.providerKind === 'custom_mcp' && form.snapshotDelivery === 'shared_dir'
        ? form.sharedRoot.trim()
        : '',
    })
    const saved = res?.data ?? res
    MessagePlugin.success(t('codeWiki.providerDialog.saved'))
    dialogVisible.value = false
    emit('saved', saved)
  } catch (err: any) {
    MessagePlugin.error(err?.message ?? t('codeWiki.loadFailed'))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.provider-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
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
