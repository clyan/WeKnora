<template>
  <section class="topic-review">
    <div v-if="detail" class="coverage" role="status">
      <p>{{ t('codeWiki.topics.coverageCount', detail.coverage) }}</p>
      <p>{{ t('codeWiki.topics.coverageHint') }}</p>
      <p v-if="detail.coverage.validation">
        {{ t('codeWiki.topics.sourceCheck') }}: {{ t(`codeWiki.topics.check_${detail.coverage.validation.source}`) }} ·
        {{ t('codeWiki.topics.citationCheck') }}: {{ t(`codeWiki.topics.check_${detail.coverage.validation.citations}`) }} ·
        {{ t('codeWiki.topics.coverageCheck') }}: {{ t(`codeWiki.topics.check_${detail.coverage.validation.coverage}`) }}
      </p>
    </div>
    <p v-if="detail?.predecessors?.length" class="analysis-origin">
      {{ t('codeWiki.topics.predecessors') }}:
      {{ detail.predecessors.map(source => `${source.key} (${t('codeWiki.topics.revision')} ${source.revision})`).join(', ') }}
    </p>
    <p v-if="detail?.successors?.length" class="analysis-origin">{{ t('codeWiki.topics.successors') }}: {{ detail.successors.join(', ') }}</p>
    <t-radio-group v-model="kind" size="small" @change="load(false)">
      <t-radio-button value="items">{{ t('codeWiki.topics.items') }}</t-radio-button>
      <t-radio-button value="evidence">{{ t('codeWiki.topics.evidence') }}</t-radio-button>
      <t-radio-button value="reviews">{{ t('codeWiki.topics.reviews') }}</t-radio-button>
    </t-radio-group>
    <p v-if="error" role="alert">{{ error }}</p>
    <t-loading v-if="loading" size="14px" />
    <p v-if="!loading && detail && !detail.total">{{ t('codeWiki.topics.empty') }}</p>
    <ul v-if="kind === 'items'">
      <li v-for="item in items" :key="item.key">
        <strong>{{ item.question }}</strong>
        <span> · {{ t(`codeWiki.topics.item_${item.status}`) }}</span>
        <p v-if="item.conclusion">{{ item.conclusion }}</p>
        <p v-if="item.condition">{{ item.condition }}</p>
        <p v-if="item.source_topic_key" class="analysis-origin">{{ t('codeWiki.topics.analysisOrigin') }}: {{ item.source_topic_key }} · {{ t('codeWiki.topics.revision') }} {{ item.source_revision }} · {{ item.source_item_key }}</p>
        <p v-if="item.consistency_peer" class="analysis-origin">{{ t('codeWiki.topics.consistencyPeer') }}: {{ item.consistency_peer.topic_key }} · {{ t('codeWiki.topics.revision') }} {{ item.consistency_peer.revision }} · {{ item.consistency_peer.item_key }}</p>
        <small v-if="item.evidence_ids?.length">{{ t('codeWiki.topics.evidence') }}: {{ item.evidence_ids.join(', ') }}</small>
      </li>
    </ul>
    <ul v-else-if="kind === 'evidence'">
      <li v-for="source in evidence" :key="source.id">
        <button type="button" class="source-link" @click="showSource(source.id)">{{ source.id }} · {{ source.path }}:{{ source.start_line }}–{{ source.end_line }}</button>
      </li>
    </ul>
    <ul v-else>
      <li v-for="review in reviews" :key="review.id">
        <strong>{{ t(`codeWiki.topics.${review.kind}`) }}</strong> · {{ t(`codeWiki.topics.${review.result}`) }}
        <p>{{ review.note }}</p>
        <small>{{ new Date(review.created_at).toLocaleString() }} · {{ review.actor_id }} · {{ t('codeWiki.topics.revision') }} {{ review.analysis_revision }}</small>
      </li>
    </ul>
    <div v-if="kind === 'evidence' && sourceView" class="source-view">
      <strong>{{ sourceView.path }}:{{ sourceView.start_line }}–{{ sourceView.end_line }}</strong>
      <pre>{{ sourceView.source }}</pre>
      <t-button v-if="sourceView.next_start != null" variant="text" size="small" :loading="sourceLoading" @click="showSource(sourceId, sourceView.next_start)">{{ t('codeWiki.topics.more') }}</t-button>
    </div>
    <t-button v-if="detail?.next_offset != null" variant="text" size="small" :loading="loading" @click="load(true)">{{ t('codeWiki.topics.more') }}</t-button>
    <fieldset v-if="canReview" class="review-form">
      <legend>{{ t('codeWiki.topics.addReview') }}</legend>
      <p>{{ t('codeWiki.topics.reviewHint') }}</p>
      <t-radio-group v-model="reviewKind" size="small">
        <t-radio-button value="clarification">{{ t('codeWiki.topics.clarification') }}</t-radio-button>
        <t-radio-button value="correction">{{ t('codeWiki.topics.correction') }}</t-radio-button>
        <t-radio-button value="sample">{{ t('codeWiki.topics.sample') }}</t-radio-button>
      </t-radio-group>
      <t-radio-group v-if="reviewKind === 'sample'" v-model="result" size="small">
        <t-radio value="passed">{{ t('codeWiki.topics.passed') }}</t-radio>
        <t-radio value="needs_changes">{{ t('codeWiki.topics.needs_changes') }}</t-radio>
      </t-radio-group>
      <t-textarea v-model="note" :maxlength="1000" :placeholder="t('codeWiki.topics.note')" />
      <t-button size="small" :loading="saving" :disabled="!note.trim() || !detail" @click="save">{{ t('codeWiki.topics.save') }}</t-button>
    </fieldset>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCodeWikiTopic, reviewCodeWikiTopic, getCodeWikiTopicSource } from '@/api/code-wiki'
import type { CodeWikiTopicDetail, CodeWikiTopicItem, CodeWikiEvidenceSpec, CodeWikiTopicReview, CodeWikiTopicSource } from '@/api/code-wiki'
const props = defineProps<{ kb: string; run: string; topic: string; revision: number; canReview: boolean }>()
const { t } = useI18n()
const kind = ref('items')
const reviewKind = ref('clarification')
const result = ref('passed')
const note = ref('')
const detail = ref<CodeWikiTopicDetail | null>(null)
const items = ref<CodeWikiTopicItem[]>([])
const evidence = ref<CodeWikiEvidenceSpec[]>([])
const reviews = ref<CodeWikiTopicReview[]>([])
const error = ref('')
const loading = ref(false)
const saving = ref(false)
let requestSeq = 0
const sourceView = ref<CodeWikiTopicSource | null>(null)
const sourceId = ref('')
const sourceLoading = ref(false)
let sourceSeq = 0
const showSource = async (id: string, start = 0) => {
  const seq = ++sourceSeq
  sourceLoading.value = true
  try {
    const res: any = await getCodeWikiTopicSource(props.kb, props.run, props.topic, id, start)
    if (seq !== sourceSeq) return
    sourceId.value = id
    sourceView.value = res?.data ?? res
  } catch { if (seq === sourceSeq) error.value = t('codeWiki.loadFailed') }
  finally { if (seq === sourceSeq) sourceLoading.value = false }
}
const load = async (append: boolean) => {
  const seq = ++requestSeq
  const offset = append ? detail.value?.next_offset ?? 0 : 0
  loading.value = true
  error.value = ''
  try {
    const res: any = await getCodeWikiTopic(props.kb, props.run, props.topic, kind.value, offset)
    if (seq !== requestSeq) return
    const next: CodeWikiTopicDetail = res?.data ?? res
    if (append && detail.value && next.topic.revision !== detail.value.topic.revision) { await load(false); return }
    if (!append) { items.value = []; evidence.value = []; reviews.value = [] }
    items.value.push(...next.items ?? [])
    evidence.value.push(...next.evidence ?? [])
    reviews.value.push(...next.reviews ?? [])
    detail.value = next
  } catch { if (seq === requestSeq) error.value = t('codeWiki.loadFailed') }
  finally { if (seq === requestSeq) loading.value = false }
}
const save = async () => {
  if (!props.canReview || !detail.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    await reviewCodeWikiTopic(props.kb, props.run, props.topic, { revision: detail.value.topic.revision, kind: reviewKind.value, result: reviewKind.value === 'sample' ? result.value : 'noted', note: note.value })
    note.value = ''
    kind.value = 'reviews'
    await load(false)
  } catch (err: any) { error.value = err?.message || t('codeWiki.loadFailed') }
  finally { saving.value = false }
}
onMounted(() => load(false))
watch(() => [props.run, props.topic, props.revision], () => {
  sourceSeq++
  sourceView.value = null
  sourceLoading.value = false
  load(false)
})
</script>

<style scoped>
.topic-review { margin-top: 16px; }
.coverage { padding: 8px 12px; margin-bottom: 12px; background: var(--td-bg-color-secondarycontainer); font-size: 12px; }
ul { padding-left: 18px; }
li { padding: 8px 0; overflow-wrap: anywhere; }
p { margin: 6px 0; }
small { color: var(--td-text-color-secondary); }
.analysis-origin { font-size: 12px; color: var(--td-text-color-secondary); overflow-wrap: anywhere; }
.review-form { margin-top: 12px; border: 1px solid var(--td-component-stroke); padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.source-link { border: 0; padding: 0; background: transparent; color: var(--td-brand-color); cursor: pointer; text-align: left; overflow-wrap: anywhere; }
.source-view pre { overflow: auto; max-height: 360px; padding: 12px; background: var(--td-bg-color-secondarycontainer); }
</style>
