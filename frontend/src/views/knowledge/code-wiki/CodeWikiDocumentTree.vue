<template>
  <ul class="document-tree">
    <li v-for="node in nodes" :key="node.key" class="document-tree-node">
      <template v-if="node.kind === 'folder'">
        <button type="button" class="document-folder" :aria-expanded="!collapsed.has(node.key)"
          @click="toggle(node.key)">
          <t-icon :name="collapsed.has(node.key) ? 'chevron-right' : 'chevron-down'" size="14px" />
          <t-icon name="folder" size="16px" />
          <span class="document-folder-title">{{ node.title }}</span>
          <span class="document-folder-count">{{ node.count }}</span>
        </button>
        <CodeWikiDocumentTree v-if="!collapsed.has(node.key)" :nodes="node.children"
          :selected-page-key="selectedPageKey">
          <template #page="{ page }"><slot name="page" :page="page" /></template>
        </CodeWikiDocumentTree>
      </template>
      <slot v-else name="page" :page="node.page" />
    </li>
  </ul>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import type { CodeWikiRunPage } from '@/api/code-wiki'
import { containsDocument, type DocumentTreeNode } from './documentTree'

const props = defineProps<{ nodes: DocumentTreeNode[]; selectedPageKey?: string }>()
defineSlots<{ page(props: { page: CodeWikiRunPage }): unknown }>()
const collapsed = ref(new Set<string>())
function toggle(key: string) {
  if (collapsed.value.has(key)) collapsed.value.delete(key)
  else collapsed.value.add(key)
}

// Selection opens its ancestor folders; status polling does not reset the
// user's collapsed folders. New folders start expanded.
watch(() => props.selectedPageKey, key => {
  if (!key) return
  for (const node of props.nodes) {
    if (node.kind === 'folder' && containsDocument(node, key)) collapsed.value.delete(node.key)
  }
}, { immediate: true })
</script>

<style lang="less" scoped>
.document-tree { list-style: none; margin: 0; padding: 0; min-width: 0; }
.document-tree-node { min-width: 0; }
.document-tree-node > .document-tree { margin-left: 10px; padding-left: 10px; border-left: 1px solid var(--td-component-stroke); }
.document-folder {
  display: flex; align-items: center; gap: 6px; width: 100%; padding: 9px 4px; border: 0;
  background: transparent; color: var(--td-text-color-secondary); text-align: left;
  cursor: pointer; font: inherit; font-size: 12px; border-radius: 4px;
  .t-icon { flex-shrink: 0; }
  &:hover { background: var(--td-bg-color-container-hover); }
  &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: -2px; }
}
.document-folder-title { min-width: 0; overflow-wrap: anywhere; font-weight: 600; }
.document-folder-count { margin-left: auto; padding-left: 6px; color: var(--td-text-color-placeholder); font-variant-numeric: tabular-nums; }
</style>
