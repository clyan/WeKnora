<template>
  <section class="topic-progress" :aria-label="t('codeWiki.topics.title')">
    <div class="topic-summary" role="status">
      <strong>{{ t('codeWiki.topics.title') }}</strong>
      <span>{{ t('codeWiki.topics.concurrency', { active: active, limit: limit || 3 }) }}</span>
      <span>{{ t('codeWiki.topics.completedCount', { completed, total: topics.length }) }}</span>
    </div>
    <p class="topic-hint">{{ t('codeWiki.topics.countHint') }}</p>
    <label class="topic-filter">{{ t('codeWiki.topics.filter') }}
      <select v-model="filter">
        <option value="all">{{ t('codeWiki.topics.all') }}</option>
        <option v-for="status in ['running', 'queued', 'waiting', 'paused', 'failed', 'completed']" :key="status" :value="status">{{ t(`codeWiki.topics.${status}`) }}</option>
      </select>
    </label>
    <details v-for="topic in visibleTopics" :key="topic.id" class="topic-row" @toggle="toggleTopic(topic.id, $event)">
      <summary>
        <t-loading v-if="working && connected && topic.status === 'running'" size="14px" />
        <t-icon v-else :name="topic.status === 'completed' ? 'check-circle' : topic.status === 'failed' ? 'error-circle' : 'time'" size="14px" />
        <span class="topic-title">{{ topic.title }}</span>
        <t-tag size="small" variant="light" :theme="topic.status === 'failed' ? 'danger' : topic.status === 'completed' ? 'success' : 'default'">
          {{ t(`codeWiki.topics.${topic.status}`) }}
        </t-tag>
      </summary>
      <div class="topic-detail">
        <p role="status">{{ topic.action || t(`codeWiki.topics.${topic.status}`) }}</p>
        <p v-if="topic.status === 'failed'">{{ t('codeWiki.topics.failedHint') }}</p>
        <t-button v-if="topic.status === 'completed'" size="small" variant="outline" @click="emit('preview', topic.topic_key)">
          {{ t('codeWiki.topics.preview') }}
        </t-button>
        <t-button v-if="canRetry && topic.status === 'failed'" size="small" variant="outline" :loading="retrying === topic.topic_key" :disabled="!!retrying" @click="emit('retry', topic.topic_key)">
          {{ t('codeWiki.topics.retry') }}
        </t-button>
        <ol v-if="eventsFor(topic).length" class="topic-events">
          <li v-for="event in eventsFor(topic)" :key="event.id">
            <time>{{ new Date(event.created_at).toLocaleTimeString() }}</time>
            <span>{{ event.message }}</span>
          </li>
        </ol>
        <details v-if="topic.error_message">
          <summary>{{ t('codeWiki.topics.details') }}</summary>
          <p class="topic-error">{{ topic.error_message }}</p>
        </details>
        <CodeWikiTopicReview v-if="expanded.has(topic.id)" :kb="kb" :run="run" :topic="topic.topic_key" :revision="topic.revision" :can-review="canReview" />
      </div>
    </details>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodeWikiTopic, CodeWikiRunEvent } from '@/api/code-wiki'
import CodeWikiTopicReview from './CodeWikiTopicReview.vue'

const props = defineProps<{
  topics: CodeWikiTopic[]
  events: CodeWikiRunEvent[]
  limit?: number
  working: boolean
  connected: boolean
  canRetry: boolean
  retrying: string
  kb: string
  run: string
  canReview: boolean
}>()
const emit = defineEmits<{ retry: [key: string]; preview: [key: string] }>()
const { t } = useI18n()
const expanded = ref(new Set<string>())
const filter = ref('all')
const visibleTopics = computed(() => {
  const priority: Record<string, number> = { running: 0, waiting: 1, failed: 2, queued: 3, paused: 4, completed: 5 }
  return props.topics.filter(topic => filter.value === 'all' || topic.status === filter.value)
    .slice().sort((a, b) => {
      // Preserve the position of an expanded topic while the user reads it.
      if (expanded.value.size) return props.topics.indexOf(a) - props.topics.indexOf(b)
      return (priority[a.status] ?? 6) - (priority[b.status] ?? 6) || a.title.localeCompare(b.title)
    })
})
const toggleTopic = (id: string, event: Event) => {
  if (event.target !== event.currentTarget) return
  if ((event.target as HTMLDetailsElement).open) expanded.value.add(id)
  else expanded.value.delete(id)
}
const active = computed(() => props.topics.filter(topic => topic.status === 'running').length)
const completed = computed(() => props.topics.filter(topic => topic.status === 'completed').length)
const eventsFor = (topic: CodeWikiTopic) => props.events.filter(event => event.topic_key === topic.topic_key).slice(0, 20)
</script>

<style scoped>
.topic-progress { margin: 16px 0; }
.topic-summary { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.topic-hint { color: var(--td-text-color-secondary); font-size: 12px; }
.topic-filter { display: flex; align-items: center; gap: 8px; margin: 12px 0; font-size: 12px; }
.topic-filter select { background: var(--td-bg-color-container); color: var(--td-text-color-primary); border: 1px solid var(--td-component-stroke); border-radius: 4px; padding: 4px 8px; }
.topic-row { border-top: 1px solid var(--td-component-stroke); padding: 10px 0; }
.topic-row > summary { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.topic-title { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.topic-detail { padding: 4px 0 4px 22px; }
.topic-events { padding-left: 18px; font-size: 12px; color: var(--td-text-color-secondary); }
.topic-events li { padding: 4px 0; }
.topic-events time { margin-right: 8px; font-variant-numeric: tabular-nums; }
.topic-error { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
