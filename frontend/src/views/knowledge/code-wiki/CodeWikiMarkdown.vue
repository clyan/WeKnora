<template>
  <div ref="element" class="md-content" v-html="html" />
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import { createChatMarkdownRenderer, renderChatMarkdown } from '@/utils/chatMarkdownRenderer'
import { safeMarkdownToHTML, sanitizeMarkdownHTML } from '@/utils/security'
import { createMermaidCodeRenderer, enhanceMarkdownContainer } from '@/utils/mermaidShared'
import 'katex/dist/katex.min.css'

const props = defineProps<{ content: string }>()
const element = ref<HTMLElement | null>(null)
const renderer = createChatMarkdownRenderer({ codeRenderer: createMermaidCodeRenderer(`codewiki-${useId().replace(/\W/g, '')}`) })
// Derive HTML from the string prop so polling the same page does not recreate
// code blocks or diagrams and interrupt reading.
const html = computed(() => renderChatMarkdown(props.content, {
  renderer, escapeMarkdown: safeMarkdownToHTML, sanitizeHtml: sanitizeMarkdownHTML,
}))
async function enhance() {
  await nextTick()
  await enhanceMarkdownContainer(element.value)
}
onMounted(enhance)
watch(html, enhance, { flush: 'post' })
</script>
