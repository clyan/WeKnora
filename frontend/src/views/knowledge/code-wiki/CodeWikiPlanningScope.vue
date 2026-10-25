<template>
  <details class="planning-scope">
    <summary>
      <span>{{ t(current ? 'codeWiki.workspace.planningScope' : 'codeWiki.workspace.recentPlanningScope') }}</span>
      <span class="scope-count">{{ t('codeWiki.workspace.planningScopeCount', { modules: batch.modules.length, files: batch.file_count }) }}</span>
    </summary>
    <div class="scope-body">
      <p class="scope-repository">{{ batch.repository_name }}</p>
      <p class="scope-hint">{{ t('codeWiki.workspace.planningScopeHint') }}</p>
      <div class="scope-modules">
        <section v-for="module in batch.modules" :key="module.id" class="scope-module">
          <h5>{{ module.directory }}</h5>
          <ul>
            <li v-for="file in module.files" :key="file">{{ file }}</li>
          </ul>
        </section>
      </div>
    </div>
  </details>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { CodeWikiPlanningBatch } from '@/api/code-wiki'

defineProps<{ batch: CodeWikiPlanningBatch; current: boolean }>()
const { t } = useI18n()
</script>

<style scoped>
.planning-scope { margin-top: 12px; font-size: 12px; overflow-wrap: anywhere; }
summary { cursor: pointer; color: var(--td-text-color-primary); }
.scope-count { margin-left: 8px; color: var(--td-text-color-secondary); }
.scope-body { margin-top: 8px; padding: 12px; border: 1px solid var(--td-component-stroke); border-radius: 6px; }
.scope-repository { margin: 0 0 6px; font-weight: 600; }
.scope-hint { margin: 0 0 12px; color: var(--td-text-color-secondary); }
.scope-modules { max-height: 320px; overflow-y: auto; }
.scope-module + .scope-module { margin-top: 12px; }
h5 { margin: 0 0 4px; font-size: 12px; font-weight: 600; }
ul { margin: 0; padding-left: 20px; }
li { padding: 2px 0; color: var(--td-text-color-secondary); }
</style>
