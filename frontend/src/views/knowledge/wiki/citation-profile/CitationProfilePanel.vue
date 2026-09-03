<template>
  <section class="citation-profile-panel" aria-labelledby="citation-profile-title">
    <header class="citation-profile-header">
      <div>
        <h2 id="citation-profile-title">My source-citation evidence</h2>
        <p>Private evidence from future committed answers after opt-in.</p>
      </div>
      <t-button size="small" variant="outline" :loading="loading" @click="loadAll">
        <template #icon><t-icon name="refresh" /></template>
        Refresh
      </t-button>
    </header>

    <t-alert v-if="errorMessage" theme="warning" :message="errorMessage" />

    <div v-if="operationReceipt" class="citation-profile-receipt" role="status">
      {{ operationReceipt }}
    </div>

    <div v-if="loading && !status" class="citation-profile-empty">
      <t-loading size="small" />
      <span>Loading evidence profile</span>
    </div>

    <template v-else-if="status">
      <div v-if="emptyKind === 'feature_disabled'" class="citation-profile-empty">
        <t-icon name="error-circle" />
        <strong>Evidence profile is off for this knowledge base.</strong>
        <span>Ordinary Wiki behavior continues with no profile requests.</span>
      </div>

      <div v-else-if="emptyKind === 'acl_unknown'" class="citation-profile-empty">
        <t-icon name="error-circle" />
        <strong>Access check is unavailable.</strong>
        <span>Collection is suspended. You can still submit the account erasure request.</span>
        <t-button size="small" theme="danger" variant="outline" :loading="blindDeleting" @click="blindDelete">
          Account erasure request
        </t-button>
      </div>

      <div v-else-if="!status.enrolled" class="citation-profile-empty">
        <t-icon name="file-add" />
        <strong>Not enrolled</strong>
        <span>Only sources cited by future committed answers after opt-in can appear here.</span>
        <span>Exact Wiki source links may resolve to zero, one, or many pages.</span>
        <t-button size="small" theme="primary" :loading="enrolling" @click="setEnrollment(true)">
          Enable my evidence profile
        </t-button>
      </div>

      <template v-else>
        <div class="citation-profile-guidance">
          <t-icon name="info-circle" />
          <span>There is not enough valid evidence to suggest a next learning step.</span>
        </div>

        <div v-if="status.snapshot && ((status.snapshot.pending_event_count || 0) || (status.snapshot.dirty_event_count || 0))"
          class="citation-profile-notice">
          <t-icon name="time" />
          <span>{{ status.snapshot.pending_event_count || 0 }} pending, {{ status.snapshot.dirty_event_count || 0 }} stale.</span>
        </div>

        <div v-if="emptyKind === 'no_events' || nodes.length === 0" class="citation-profile-empty">
          <t-icon name="file-unknown" />
          <strong>No citation evidence yet</strong>
          <span>New qualifying answer references will appear after resolution.</span>
        </div>

        <template v-else>
          <div v-if="graphNotice" class="citation-profile-notice">
            <t-icon name="chart-bubble" />
            <span>{{ graphNotice.text }}</span>
            <a href="#" @click.prevent="focusNodeList">Complete node list</a>
          </div>

          <div ref="nodeListRef" class="citation-profile-list" role="list" tabindex="-1"
            aria-label="Complete citation evidence node list">
            <button v-for="node in nodes" :key="node.page_uuid" type="button" class="citation-profile-node" role="listitem"
              :aria-label="`${node.title}: ${overlayLabel(node.overlay)}`" @click="openEvidence(node)">
              <span class="citation-profile-node-main">
                <strong>{{ node.title }}</strong>
                <span>{{ node.slug }}</span>
              </span>
              <span class="citation-profile-node-meta">
                <t-tag size="small" :theme="overlayTheme(node.overlay)" variant="light">{{ overlayLabel(node.overlay) }}</t-tag>
                <span>{{ node.authorized_evidence_count }} source-reference events</span>
              </span>
            </button>
          </div>

          <t-button v-if="nextCursor" size="small" variant="outline" :loading="nodesLoading" @click="loadNodes(nextCursor)">
            Load more
          </t-button>
        </template>

        <div class="citation-profile-rights" aria-label="Profile rights actions">
          <t-button size="small" variant="outline" :loading="exporting" @click="startExport">
            <template #icon><t-icon name="download" /></template>
            Export profile
          </t-button>
          <t-button size="small" theme="danger" variant="outline" :loading="deleting" @click="deleteCurrent">
            <template #icon><t-icon name="delete" /></template>
            Delete profile
          </t-button>
          <t-button size="small" theme="danger" variant="text" :loading="blindDeleting" @click="blindDelete">
            Account erasure request
          </t-button>
        </div>

      </template>
    </template>

    <t-drawer v-model:visible="drawerVisible" :header="drawerTitle" size="520px" :footer="false" destroy-on-close>
      <div v-if="evidence" class="citation-profile-drawer">
        <p class="citation-profile-claim">This answer cited a source linked to this page at resolution time.</p>
        <dl class="citation-profile-details">
          <div>
            <dt>Page UUID</dt>
            <dd>{{ evidence.page.page_uuid }}</dd>
          </div>
          <div>
            <dt>Page version</dt>
            <dd>{{ evidence.page.page_version }}</dd>
          </div>
          <div>
            <dt>Snapshot read version</dt>
            <dd>{{ evidence.snapshot?.read_version || '0' }}</dd>
          </div>
        </dl>

        <div v-for="item in evidence.items" :key="item.event_id" class="citation-profile-evidence-item">
          <dl class="citation-profile-details">
            <div>
              <dt>Answer occurrence time</dt>
              <dd>{{ item.occurred_at }}</dd>
            </div>
            <div>
              <dt>Relation resolution time</dt>
              <dd>{{ item.resolved_at }}</dd>
            </div>
            <div>
              <dt>Message ID</dt>
              <dd>{{ item.message_id }}</dd>
            </div>
            <div>
              <dt>Source knowledge ID</dt>
              <dd>{{ item.source_knowledge_id }}</dd>
            </div>
            <div>
              <dt>Mapping revision</dt>
              <dd>{{ item.run_mapping_revision }}</dd>
            </div>
            <div>
              <dt>Captured page version</dt>
              <dd>{{ item.page_version_at_resolution }}</dd>
            </div>
            <div>
              <dt>Correction state</dt>
              <dd>{{ item.correction_state }}</dd>
            </div>
          </dl>

          <t-alert v-if="item.stale_mapping" theme="warning" message="Evidence mapping is stale; refresh before action." />

          <div class="citation-profile-corrections">
            <t-select v-model="reasonCode" size="small" class="citation-profile-reason">
              <t-option value="wrong_page" label="Wrong page" />
              <t-option value="stale_mapping" label="Stale mapping" />
              <t-option value="other" label="Other" />
            </t-select>
            <t-button size="small" :disabled="isActionBlocked" @click="correct(item, 'confirm_relevant')">
              Confirm this mapping
            </t-button>
            <t-button size="small" theme="warning" variant="outline" :disabled="isActionBlocked" @click="correct(item, 'reject_mapping')">
              Reject this mapping
            </t-button>
            <t-button size="small" theme="danger" variant="outline" :disabled="isActionBlocked" @click="correct(item, 'retract_event')">
              Retract this source event
            </t-button>
          </div>
        </div>

        <t-button v-if="evidence.next_cursor" size="small" variant="outline" :loading="evidenceLoading"
          @click="loadEvidencePage(evidence.page.page_uuid, evidence.next_cursor)">
          Load more evidence
        </t-button>
      </div>
    </t-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  citationProfileClient,
  type CitationProfileCorrectionAction,
  type CitationProfileEvidenceItem,
  type CitationProfileEvidenceResponse,
  type CitationProfileGraphResponse,
  type CitationProfileNode,
  type CitationProfileStatusResponse,
} from '@/api/wiki/citationProfile'
import {
  correctionNeedsRefresh,
  graphCapNotice,
  makeCitationProfileIdempotencyKey,
} from './model'

const props = defineProps<{ knowledgeBaseId: string }>()

const loading = ref(false)
const nodesLoading = ref(false)
const evidenceLoading = ref(false)
const enrolling = ref(false)
const exporting = ref(false)
const deleting = ref(false)
const blindDeleting = ref(false)
const errorMessage = ref('')
const status = ref<CitationProfileStatusResponse | null>(null)
const nodes = ref<CitationProfileNode[]>([])
const nextCursor = ref<string | null>(null)
const graph = ref<CitationProfileGraphResponse | null>(null)
const evidence = ref<CitationProfileEvidenceResponse | null>(null)
const drawerVisible = ref(false)
const selectedNode = ref<CitationProfileNode | null>(null)
const nodeListRef = ref<HTMLElement | null>(null)
const reasonCode = ref('wrong_page')
const operationReceipt = ref('')

const emptyKind = computed(() => status.value?.empty_state?.kind)
const drawerTitle = computed(() => evidence.value?.page.title || selectedNode.value?.title || 'Evidence')
const graphNotice = computed(() => graph.value ? graphCapNotice(graph.value) : null)
const currentReadVersion = computed(() => status.value?.snapshot?.read_version || evidence.value?.snapshot?.read_version || null)
const evidenceReadVersion = computed(() => evidence.value?.snapshot?.read_version || null)
const isActionBlocked = computed(() => correctionNeedsRefresh(evidenceReadVersion.value, currentReadVersion.value))

function readPayload<T>(response: T | { data: T }): T {
  return ((response as { data?: T })?.data || response) as T
}

async function loadAll() {
  if (!props.knowledgeBaseId) return
  loading.value = true
  errorMessage.value = ''
  try {
    status.value = readPayload(await citationProfileClient.getStatus(props.knowledgeBaseId))
    nodes.value = []
    nextCursor.value = null
    graph.value = null
    if (status.value.enrolled && !status.value.suspended) {
      await Promise.all([loadNodes(null), loadGraph()])
    }
  } catch (error: any) {
    errorMessage.value = error?.message || 'Evidence profile is unavailable.'
  } finally {
    loading.value = false
  }
}

async function loadGraph() {
  graph.value = readPayload(await citationProfileClient.getGraph(props.knowledgeBaseId))
}

async function loadNodes(cursor: string | null) {
  nodesLoading.value = true
  try {
    const page = readPayload(await citationProfileClient.listNodes(props.knowledgeBaseId, {
      cursor,
      page_size: status.value?.limits.list_page_size || 100,
    }))
    nodes.value = cursor ? [...nodes.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } finally {
    nodesLoading.value = false
  }
}

async function setEnrollment(enabled: boolean) {
  enrolling.value = true
  try {
    await citationProfileClient.updateEnrollment(props.knowledgeBaseId, {
      enabled,
      expected_read_version: status.value?.snapshot?.read_version || '0',
      idempotency_key: makeCitationProfileIdempotencyKey(),
    })
    await loadAll()
  } catch (error: any) {
    MessagePlugin.error(error?.message || 'Enrollment update failed.')
  } finally {
    enrolling.value = false
  }
}

async function openEvidence(node: CitationProfileNode) {
  selectedNode.value = node
  drawerVisible.value = true
  evidence.value = null
  await loadEvidencePage(node.page_uuid, null)
}

async function loadEvidencePage(pageUuid: string, cursor: string | null) {
  evidenceLoading.value = true
  try {
    const page = readPayload(await citationProfileClient.getEvidence(props.knowledgeBaseId, pageUuid, {
      cursor,
      page_size: status.value?.limits.list_page_size || 100,
    }))
    evidence.value = evidence.value && cursor
      ? { ...page, items: [...evidence.value.items, ...page.items] }
      : page
  } catch (error: any) {
    MessagePlugin.error(error?.message || 'Evidence load failed.')
  } finally {
    evidenceLoading.value = false
  }
}

async function correct(item: CitationProfileEvidenceItem, action: CitationProfileCorrectionAction) {
  if (!evidence.value || !evidence.value.snapshot || isActionBlocked.value) return
  const pageUuid = item.page_uuid
  try {
    const result = readPayload(await citationProfileClient.createCorrection(props.knowledgeBaseId, {
      expected_read_version: evidence.value.snapshot.read_version,
      idempotency_key: makeCitationProfileIdempotencyKey(),
      action,
      event_id: item.event_id,
      page_uuid: pageUuid,
      reason_code: reasonCode.value,
    }))
    if (status.value?.snapshot) status.value.snapshot.read_version = result.snapshot.read_version
    await loadAll()
    if (drawerVisible.value) {
      await loadEvidencePage(pageUuid, null)
    }
    MessagePlugin.success('Correction applied.')
  } catch (error: any) {
    if (error?.code === 'profile_changed' || error?.status === 409) {
      MessagePlugin.warning('Evidence changed. Refresh and review again.')
      return
    }
    MessagePlugin.error(error?.message || 'Correction failed.')
  }
}

async function startExport() {
  exporting.value = true
  try {
    const result = readPayload(await citationProfileClient.createExport(props.knowledgeBaseId, {
      expected_read_version: currentReadVersion.value || '0',
      idempotency_key: makeCitationProfileIdempotencyKey(),
      format: 'json',
    }))
    operationReceipt.value = result.status === 'ready'
      ? 'Export is ready for reauthorized download.'
      : 'Export is preparing and expires in one hour.'
    if (result.status === 'ready') {
      const blob = await citationProfileClient.downloadExport(props.knowledgeBaseId, result.operation_id)
      saveExportBlob(blob, result.operation_id)
      operationReceipt.value = 'Export downloaded.'
    }
  } catch (error: any) {
    MessagePlugin.error(error?.message || 'Export failed.')
  } finally {
    exporting.value = false
  }
}

async function deleteCurrent() {
  deleting.value = true
  try {
    await citationProfileClient.deleteCurrentProfile(props.knowledgeBaseId, {
      expected_read_version: currentReadVersion.value || '0',
      idempotency_key: makeCitationProfileIdempotencyKey(),
    })
    drawerVisible.value = false
    nodes.value = []
    graph.value = null
    operationReceipt.value = 'Profile hidden immediately; active-plane purge scheduled.'
    await loadAll()
  } catch (error: any) {
    MessagePlugin.error(error?.message || 'Delete request failed.')
  } finally {
    deleting.value = false
  }
}

async function blindDelete() {
  blindDeleting.value = true
  try {
    await citationProfileClient.blindDeleteScope(props.knowledgeBaseId)
    drawerVisible.value = false
    nodes.value = []
    graph.value = null
    operationReceipt.value = 'Request accepted.'
  } catch (error: any) {
    MessagePlugin.error(error?.message || 'Request failed.')
  } finally {
    blindDeleting.value = false
  }
}

function saveExportBlob(blob: Blob, operationId: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `citation-profile-${operationId}.json`
  anchor.rel = 'noopener'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

function focusNodeList() {
  nodeListRef.value?.focus()
}

function overlayLabel(overlay: CitationProfileNode['overlay']): string {
  switch (overlay) {
    case 'evidenced_current':
      return 'Current-version source evidence'
    case 'evidenced_historical':
      return 'Earlier-version source evidence'
    case 'disputed':
      return 'You disputed this mapping'
    default:
      return 'No citation evidence'
  }
}

function overlayTheme(overlay: CitationProfileNode['overlay']) {
  if (overlay === 'disputed') return 'warning'
  if (overlay === 'unknown') return 'default'
  return 'success'
}

onMounted(loadAll)
</script>

<style scoped lang="less">
.citation-profile-panel {
  flex: 0 0 360px;
  width: 360px;
  max-width: 42vw;
  min-width: 300px;
  height: 100%;
  overflow: auto;
  border-left: 1px solid var(--td-component-stroke);
  background: var(--td-bg-color-container);
  padding: 14px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.citation-profile-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;

  h2 {
    margin: 0;
    font-size: 15px;
    line-height: 20px;
    font-weight: 600;
  }

  p {
    margin: 4px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
    line-height: 18px;
  }
}

.citation-profile-empty,
.citation-profile-guidance,
.citation-profile-notice,
.citation-profile-receipt {
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  color: var(--td-text-color-secondary);
  font-size: 13px;
  line-height: 18px;
}

.citation-profile-guidance,
.citation-profile-notice,
.citation-profile-receipt {
  flex-direction: row;
  align-items: center;
}

.citation-profile-notice a {
  color: var(--td-brand-color);
  text-decoration: none;
  margin-left: auto;
}

.citation-profile-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  outline: none;
}

.citation-profile-node {
  width: 100%;
  min-height: 72px;
  padding: 10px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  display: flex;
  flex-direction: column;
  gap: 8px;
  text-align: left;
  cursor: pointer;
}

.citation-profile-node:hover,
.citation-profile-node:focus-visible {
  border-color: var(--td-brand-color);
}

.citation-profile-node-main,
.citation-profile-node-meta {
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.citation-profile-node-main strong,
.citation-profile-node-main span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.citation-profile-node-main span,
.citation-profile-node-meta {
  color: var(--td-text-color-secondary);
  font-size: 12px;
}

.citation-profile-rights,
.citation-profile-corrections {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.citation-profile-drawer {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.citation-profile-claim {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
  color: var(--td-text-color-primary);
  background: var(--td-bg-color-secondarycontainer);
}

.citation-profile-details {
  display: grid;
  grid-template-columns: 1fr;
  gap: 8px;
  margin: 0;
}

.citation-profile-details div {
  display: grid;
  grid-template-columns: 150px minmax(0, 1fr);
  gap: 8px;
  font-size: 12px;
  line-height: 18px;
}

.citation-profile-details dt {
  color: var(--td-text-color-placeholder);
}

.citation-profile-details dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--td-text-color-primary);
}

.citation-profile-evidence-item {
  display: flex;
  flex-direction: column;
  gap: 12px;
  border-top: 1px solid var(--td-component-stroke);
  padding-top: 14px;
}

.citation-profile-reason {
  width: 150px;
}

@media (max-width: 900px) {
  .citation-profile-panel {
    flex-basis: 100%;
    width: 100%;
    max-width: none;
    height: auto;
    border-left: 0;
    border-top: 1px solid var(--td-component-stroke);
  }
}
</style>
