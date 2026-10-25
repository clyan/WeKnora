<template>
  <t-dialog v-model:visible="visible" :header="$t('codeWiki.workspace.documentPreview')"
    :footer="false" width="min(1280px, calc(100vw - 32px))" top="5vh" :z-index="2800"
    attach="body" destroy-on-close class="codewiki-document-dialog" @opened="enhancePreview">
    <div class="document-reader">
      <nav class="document-nav" :aria-label="$t('codeWiki.workspace.candidateDocuments')">
        <div class="document-nav-title">{{ $t('codeWiki.workspace.candidateDocuments') }} <span>{{ pages.length }}</span></div>
        <div class="document-nav-items">
          <CodeWikiDocumentTree :nodes="documentTree" :selected-page-key="pageKey">
            <template #page="{ page }">
              <button type="button"
                :class="['document-nav-item', { selected: pageKey === page.page_key }]"
                :aria-current="pageKey === page.page_key ? 'page' : undefined" @click="pageKey = page.page_key">
                <t-loading v-if="pageRunning(page)" size="16px" />
                <t-icon v-else :name="page.generation_error ? 'error-circle' : page.content ? 'file' : 'time'" size="16px" />
                <span>{{ page.title || page.page_key }}</span>
              </button>
            </template>
          </CodeWikiDocumentTree>
        </div>
      </nav>
      <main ref="readingPane" class="document-reading-pane">
        <template v-if="selectedPage">
          <header class="document-header">
            <p v-if="selectedPage.category_path?.length" class="document-path">{{ selectedPage.category_path.join(' / ') }}</p>
            <h2>{{ selectedPage.title || selectedPage.page_key }}</h2>
            <div class="document-meta">
              <t-tag size="small" variant="light" :theme="selectedPage.generation_error ? 'danger' : selectedPage.status === 'pending' ? 'default' : 'success'">
                {{ selectedTopic ? $t(`codeWiki.topics.${selectedTopic.status}`) : $t(selectedPage.generation_error ? 'codeWiki.runs.pageFailed' : selectedPage.status === 'pending' ? 'codeWiki.runs.pagePending' : 'codeWiki.runs.pageDone') }}
              </t-tag>
              <p>{{ $t('codeWiki.workspace.candidateHint') }}</p>
            </div>
          </header>
          <p v-if="selectedPage.generation_error" class="document-error" role="alert">{{ selectedPage.generation_error }}</p>
          <p v-if="selectedPage.baseline_changed" class="document-error">{{ $t('codeWiki.workspace.baselineChanged') }}</p>
          <article v-if="selectedPage.content" :key="selectedPage.page_key" class="document-body">
            <CodeWikiMarkdown :content="selectedPage.content" />
          </article>
          <div v-else class="document-empty" role="status">
            <t-loading v-if="pageRunning(selectedPage)" size="24px" />
            <t-icon v-else name="file" size="28px" />
            <p v-if="selectedTopic">{{ selectedTopic.action || $t(`codeWiki.topics.${selectedTopic.status}`) }}</p>
            <p v-else>{{ $t(selectedPage.status === 'skipped' && newPages?.[selectedPage.page_key]
              ? 'codeWiki.workspace.notCreated'
              : ['retrieving', 'generating', 'validating'].includes(selectedPage.validation_status)
                ? `codeWiki.workspace.${selectedPage.validation_status}` : 'codeWiki.runs.pagePending') }}</p>
          </div>
          <section v-if="selectedPage.evidence?.length" class="document-support">
            <details>
              <summary>{{ $t('codeWiki.workspace.sourceEvidence') }} · {{ selectedPage.evidence.length }}</summary>
              <ul class="document-evidence">
                <li v-for="evidence in selectedPage.evidence" :key="evidence.id">
                  <strong>[{{ evidence.id }}]</strong>
                  <code>{{ evidence.path }}:{{ evidence.start_line }}–{{ evidence.end_line }}</code>
                  <span>{{ evidence.commit_sha?.slice(0, 8) }}</span>
                </li>
              </ul>
            </details>
          </section>
          <section v-if="selectedPage.section_changes?.length || selectedPage.baseline_content !== undefined" class="document-support">
            <details v-for="change in selectedPage.section_changes || []" :key="change.id" @toggle="enhancePreview">
              <summary>{{ change.title }} · {{ $t('codeWiki.workspace.chapterChanges') }}</summary>
              <div class="document-comparison">
                <section><h3>{{ $t('codeWiki.workspace.beforeChange') }}</h3><CodeWikiMarkdown :content="change.before" /></section>
                <section><h3>{{ $t('codeWiki.workspace.afterChange') }}</h3><CodeWikiMarkdown :content="change.after" /></section>
              </div>
            </details>
            <details v-if="selectedPage.baseline_content !== undefined" @toggle="enhancePreview">
              <summary>{{ $t('codeWiki.workspace.baselineContent') }}</summary>
              <CodeWikiMarkdown :content="selectedPage.baseline_content" />
            </details>
          </section>
        </template>
      </main>
    </div>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { CodeWikiRunPage, CodeWikiTopic } from '@/api/code-wiki'
import { enhanceMarkdownContainer } from '@/utils/mermaidShared'
import CodeWikiMarkdown from './CodeWikiMarkdown.vue'
import CodeWikiDocumentTree from './CodeWikiDocumentTree.vue'
import { buildDocumentTree } from './documentTree'

const props = defineProps<{ pages: CodeWikiRunPage[]; topics?: CodeWikiTopic[]; working: boolean; newPages?: Record<string, string> }>()
const visible = defineModel<boolean>('visible', { default: false })
const pageKey = defineModel<string>('pageKey', { default: '' })
const selectedPage = computed(() => props.pages.find(page => page.page_key === pageKey.value))
const selectedTopic = computed(() => props.topics?.find(topic => topic.topic_key === pageKey.value))
const pageRunning = (page: CodeWikiRunPage) => props.working && (props.topics?.length
  ? props.topics.some(topic => topic.topic_key === page.page_key && topic.status === 'running')
  : page.status === 'pending' && ['retrieving', 'generating', 'validating'].includes(page.validation_status))
const documentTree = computed(() => buildDocumentTree(props.pages))
const readingPane = ref<HTMLElement | null>(null)

async function enhancePreview() {
  if (!visible.value) return
  await nextTick()
  await enhanceMarkdownContainer(readingPane.value)
}

watch(pageKey, () => {
  readingPane.value?.scrollTo({ top: 0 })
})
</script>

<style lang="less" scoped>
@import '@/components/css/markdown.less';

.document-reader { display: grid; grid-template-columns: 280px minmax(0, 1fr); height: 78vh; min-height: 0; overflow: hidden; }
.document-nav { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--td-component-stroke); background: var(--td-bg-color-secondarycontainer); }
.document-nav-title { display: flex; justify-content: space-between; padding: 18px 16px; font-size: 13px; font-weight: 600; }
.document-nav-items { min-height: 0; overflow-y: auto; padding: 0 8px 16px; }
.document-nav-item {
  display: flex; align-items: flex-start; gap: 8px; width: 100%; border: 0; border-radius: 6px; padding: 10px;
  background: transparent; color: var(--td-text-color-secondary); cursor: pointer; text-align: left; font: inherit; font-size: 13px; line-height: 1.5;
  .t-icon, .t-loading { flex-shrink: 0; margin-top: 2px; }
  span { overflow-wrap: anywhere; }
  &:hover { background: var(--td-bg-color-container-hover); }
  &.selected { background: var(--td-brand-color-light); color: var(--td-brand-color); font-weight: 600; }
  &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: -2px; }
}
.document-reading-pane { min-width: 0; min-height: 0; overflow-y: auto; padding: 28px 40px 48px; }
.document-header { margin-bottom: 24px; border-bottom: 1px solid var(--td-component-stroke); padding-bottom: 20px;
  h2 { margin: 0 0 12px; font-size: 24px; line-height: 1.4; overflow-wrap: anywhere; }
}
.document-path { margin: 0 0 10px; color: var(--td-text-color-placeholder); font-size: 12px; overflow-wrap: anywhere; }
.document-meta { display: flex; align-items: center; gap: 10px; color: var(--td-text-color-secondary); font-size: 12px; p { margin: 0; } }
.document-error { color: var(--td-error-color); font-size: 13px; overflow-wrap: anywhere; }
.document-body { min-width: 0; }
.document-empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 80px 20px; color: var(--td-text-color-secondary); }
.document-support { border-top: 1px solid var(--td-component-stroke); margin-top: 28px; padding-top: 16px;
  details + details { margin-top: 12px; }
  summary { cursor: pointer; font-size: 13px; color: var(--td-text-color-secondary); padding: 8px 0; }
}
.document-evidence { list-style: none; padding: 0; font-size: 12px;
  li { display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 0; overflow-wrap: anywhere; }
  span { color: var(--td-text-color-placeholder); }
}
.document-comparison { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px;
  section { min-width: 0; } h3 { font-size: 13px; color: var(--td-text-color-secondary); }
}
:deep(.md-content) {
  font-size: 14px; line-height: 1.8; overflow-wrap: anywhere;
  h1 { font-size: 26px; margin: 28px 0 16px; } h2 { font-size: 21px; margin: 28px 0 14px; }
  h3 { font-size: 18px; margin: 24px 0 12px; } h4, h5, h6 { font-size: 15px; margin: 20px 0 10px; }
  p, ul, ol, table, pre, blockquote { margin-top: 12px; margin-bottom: 16px; }
  pre { overflow-x: auto; white-space: pre; max-width: 100%; }
  pre code { white-space: pre; word-break: normal; }
  table { display: block; overflow-x: auto; max-width: 100%; }
  img { max-width: 100%; height: auto; }
  .chat-code-block, .chat-mermaid-block { border: 1px solid var(--td-component-stroke); border-radius: 8px; overflow: hidden; margin: 16px 0; }
  .chat-code-block__header, .chat-mermaid-block__header {
    display: flex; align-items: center; justify-content: space-between; padding: 8px 12px;
    background: var(--td-bg-color-secondarycontainer); color: var(--td-text-color-secondary); font-size: 12px;
  }
  .chat-code-block__copy, .chat-mermaid-block__expand {
    display: inline-flex; align-items: center; gap: 5px; border: 0; border-radius: 4px; padding: 4px 6px;
    background: transparent; color: inherit; cursor: pointer; font: inherit;
    &:hover { background: var(--td-bg-color-container-hover); }
  }
  .chat-code-block__pre, .chat-mermaid-block__canvas { margin: 0; padding: 16px; border-radius: 0; }
  .chat-code-block__pre code { background: transparent; padding: 0; }
}
@media (max-width: 760px) {
  .document-reader { grid-template-columns: 160px minmax(0, 1fr); }
  .document-reading-pane { padding: 20px; }
  .document-comparison { grid-template-columns: minmax(0, 1fr); }
  .document-meta { align-items: flex-start; flex-direction: column; }
}
@media (max-width: 480px) {
  .document-reader { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }
  .document-nav { border-right: 0; border-bottom: 1px solid var(--td-component-stroke); max-height: 150px; }
  .document-nav-title { padding: 10px 16px; }
  .document-reading-pane { padding: 16px; }
}
</style>

<style lang="less">
.codewiki-document-dialog {
  .t-dialog { padding: 0; overflow: hidden; }
  .t-dialog__header { margin: 0; padding: 18px 24px; border-bottom: 1px solid var(--td-component-stroke); }
  .t-dialog__body { padding: 0; }
}
</style>
