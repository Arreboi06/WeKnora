<template>
  <section class="citation-profile-panel" aria-labelledby="citation-profile-title">
    <header class="citation-profile-header">
      <div>
        <h2 id="citation-profile-title">我的引用来源证据</h2>
        <p>仅展示开启后新回答产生的私有引用证据。</p>
      </div>
      <t-button size="small" variant="outline" :loading="loading" @click="loadAll">
        <template #icon><t-icon name="refresh" /></template>
        刷新
      </t-button>
    </header>

    <t-alert v-if="errorMessage" theme="warning" :message="errorMessage" />

    <div v-if="operationReceipt" class="citation-profile-receipt" role="status">
      {{ operationReceipt }}
    </div>

    <div v-if="loading && !status" class="citation-profile-empty">
      <t-loading size="small" />
      <span>正在加载证据画像</span>
    </div>

    <template v-else-if="status">
      <div v-if="panelState === 'deleted'" class="citation-profile-empty">
        <t-icon name="check-circle" />
        <strong>画像已停用</strong>
        <span>历史证据已从界面隐藏；审计记录按系统保留策略处理。</span>
        <t-button size="small" theme="primary" :loading="enrolling" @click="setEnrollment(true)">
          重新开启我的证据画像
        </t-button>
      </div>

      <div v-else-if="panelState === 'feature_disabled'" class="citation-profile-empty">
        <t-icon name="error-circle" />
        <strong>当前知识库未开启证据画像。</strong>
        <span>普通 Wiki 功能不受影响。</span>
      </div>

      <div v-else-if="panelState === 'acl_unknown'" class="citation-profile-empty">
        <t-icon name="error-circle" />
        <strong>访问检查暂不可用。</strong>
        <span>采集已暂停，仍可提交账号清除请求。</span>
        <t-button size="small" theme="danger" variant="outline" :loading="blindDeleting" @click="blindDelete">
          账号清除请求
        </t-button>
      </div>

      <div v-else-if="panelState === 'not_enrolled'" class="citation-profile-empty">
        <t-icon name="file-add" />
        <strong>未开启</strong>
        <span>开启后，新回答引用过的来源才会出现在这里。</span>
        <span>同一来源可能匹配 0 个、1 个或多个 Wiki 页面。</span>
        <t-button size="small" theme="primary" :loading="enrolling" @click="setEnrollment(true)">
          开启我的证据画像
        </t-button>
      </div>

      <template v-else>
        <div class="citation-profile-guidance">
          <t-icon name="info-circle" />
          <span>当前证据不足，仅展示证据，不生成下一步判断。</span>
        </div>

        <div v-if="status.snapshot && ((status.snapshot.pending_event_count || 0) || (status.snapshot.dirty_event_count || 0))"
          class="citation-profile-notice">
          <t-icon name="time" />
          <span>{{ status.snapshot.pending_event_count || 0 }} 条待处理，{{ status.snapshot.dirty_event_count || 0 }} 条需刷新。</span>
        </div>

        <div v-if="emptyKind === 'no_events' || nodes.length === 0" class="citation-profile-empty">
          <t-icon name="file-unknown" />
          <strong>暂无引用证据</strong>
          <span>新回答产生并完成解析后会显示在这里。</span>
        </div>

        <template v-else>
          <div v-if="graphNotice" class="citation-profile-notice">
            <t-icon name="chart-bubble" />
            <span>{{ graphNotice.text }}</span>
            <a href="#" @click.prevent="focusNodeList">查看完整节点列表</a>
          </div>

          <div ref="nodeListRef" class="citation-profile-list" role="list" tabindex="-1"
            aria-label="完整引用证据节点列表">
            <button v-for="node in nodes" :key="node.page_uuid" type="button" class="citation-profile-node" role="listitem"
              :aria-label="`${node.title}: ${overlayLabel(node.overlay)}`" @click="openEvidence(node)">
              <span class="citation-profile-node-main">
                <strong>{{ node.title }}</strong>
                <span>{{ node.slug }}</span>
              </span>
              <span class="citation-profile-node-meta">
                <t-tag size="small" :theme="overlayTheme(node.overlay)" variant="light">{{ overlayLabel(node.overlay) }}</t-tag>
                <span>{{ node.authorized_evidence_count }} 条来源引用事件</span>
              </span>
            </button>
          </div>

          <t-button v-if="nextCursor" size="small" variant="outline" :loading="nodesLoading" @click="loadNodes(nextCursor)">
            加载更多
          </t-button>
        </template>

        <div class="citation-profile-rights" aria-label="画像数据操作">
          <t-button size="small" variant="outline" :loading="exporting" @click="startExport">
            <template #icon><t-icon name="download" /></template>
            导出画像
          </t-button>
          <t-button size="small" theme="danger" variant="outline" :loading="deleting" @click="deleteCurrent">
            <template #icon><t-icon name="delete" /></template>
            删除画像
          </t-button>
          <t-button size="small" theme="danger" variant="text" :loading="blindDeleting" @click="blindDelete">
            账号清除请求
          </t-button>
        </div>

      </template>
    </template>

    <t-drawer v-model:visible="drawerVisible" :header="drawerTitle" size="520px" :footer="false" destroy-on-close>
      <div v-if="evidence" class="citation-profile-drawer">
        <p class="citation-profile-claim">这条回答在解析时引用了已关联到此页面的来源。</p>
        <dl class="citation-profile-details">
          <div>
            <dt>页面 UUID</dt>
            <dd>{{ evidence.page.page_uuid }}</dd>
          </div>
          <div>
            <dt>页面版本</dt>
            <dd>{{ evidence.page.page_version }}</dd>
          </div>
          <div>
            <dt>快照读取版本</dt>
            <dd>{{ evidence.snapshot?.read_version || '0' }}</dd>
          </div>
        </dl>

        <div v-for="item in evidence.items" :key="item.event_id" class="citation-profile-evidence-item">
          <dl class="citation-profile-details">
            <div>
              <dt>回答发生时间</dt>
              <dd>{{ item.occurred_at }}</dd>
            </div>
            <div>
              <dt>关系解析时间</dt>
              <dd>{{ item.resolved_at }}</dd>
            </div>
            <div>
              <dt>消息 ID</dt>
              <dd>{{ item.message_id }}</dd>
            </div>
            <div>
              <dt>来源知识 ID</dt>
              <dd>{{ item.source_knowledge_id }}</dd>
            </div>
            <div>
              <dt>映射版本</dt>
              <dd>{{ item.run_mapping_revision }}</dd>
            </div>
            <div>
              <dt>采集时页面版本</dt>
              <dd>{{ item.page_version_at_resolution }}</dd>
            </div>
            <div>
              <dt>纠正状态</dt>
              <dd>{{ correctionStateLabel(item.correction_state) }}</dd>
            </div>
          </dl>

          <t-alert v-if="item.stale_mapping" theme="warning" message="证据映射已过期，请刷新后再操作。" />

          <div class="citation-profile-corrections">
            <t-select v-model="reasonCode" size="small" class="citation-profile-reason">
              <t-option value="wrong_page" label="页面不匹配" />
              <t-option value="stale_mapping" label="映射已过期" />
              <t-option value="other" label="其他" />
            </t-select>
            <t-button size="small" :disabled="isActionBlocked" @click="correct(item, 'confirm_relevant')">
              确认这个映射
            </t-button>
            <t-button size="small" theme="warning" variant="outline" :disabled="isActionBlocked" @click="correct(item, 'reject_mapping')">
              拒绝这个映射
            </t-button>
            <t-button size="small" theme="danger" variant="outline" :disabled="isActionBlocked" @click="correct(item, 'retract_event')">
              撤回这条来源事件
            </t-button>
          </div>
        </div>

        <t-button v-if="evidence.next_cursor" size="small" variant="outline" :loading="evidenceLoading"
          @click="loadEvidencePage(evidence.page.page_uuid, evidence.next_cursor)">
          加载更多证据
        </t-button>
      </div>
    </t-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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
  CitationProfileRequestFence,
  citationProfilePanelState,
  graphCapNotice,
  makeCitationProfileIdempotencyKey,
} from './model'

const props = defineProps<{ knowledgeBaseId: string }>()
const requestFence = new CitationProfileRequestFence()

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
const panelState = computed(() => status.value ? citationProfilePanelState(status.value) : null)
const drawerTitle = computed(() => evidence.value?.page.title || selectedNode.value?.title || '证据详情')
const graphNotice = computed(() => graph.value ? graphCapNotice(graph.value) : null)
const currentReadVersion = computed(() => status.value?.snapshot?.read_version || evidence.value?.snapshot?.read_version || null)
const evidenceReadVersion = computed(() => evidence.value?.snapshot?.read_version || null)
const isActionBlocked = computed(() => correctionNeedsRefresh(evidenceReadVersion.value, currentReadVersion.value))

function readPayload<T>(response: T | { data: T }): T {
  return ((response as { data?: T })?.data || response) as T
}

function resetPanelState() {
  loading.value = false
  nodesLoading.value = false
  evidenceLoading.value = false
  enrolling.value = false
  exporting.value = false
  deleting.value = false
  blindDeleting.value = false
  errorMessage.value = ''
  status.value = null
  nodes.value = []
  nextCursor.value = null
  graph.value = null
  evidence.value = null
  drawerVisible.value = false
  selectedNode.value = null
  reasonCode.value = 'wrong_page'
  operationReceipt.value = ''
}

async function loadAll() {
  const knowledgeBaseId = props.knowledgeBaseId
  requestFence.reset(knowledgeBaseId)
  resetPanelState()
  if (!knowledgeBaseId) return null
  const token = requestFence.begin('status')
  loading.value = true
  try {
    const nextStatus = readPayload(await citationProfileClient.getStatus(knowledgeBaseId))
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return token
    status.value = nextStatus
    if (nextStatus.enrolled && !nextStatus.suspended) {
      await Promise.all([loadNodes(null), loadGraph()])
    }
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      errorMessage.value = error?.message || '证据画像暂不可用。'
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) loading.value = false
  }
  return token
}

async function loadGraph() {
  const knowledgeBaseId = props.knowledgeBaseId
  const token = requestFence.begin('graph')
  const nextGraph = readPayload(await citationProfileClient.getGraph(knowledgeBaseId))
  if (requestFence.isCurrent(token, props.knowledgeBaseId)) graph.value = nextGraph
}

async function loadNodes(cursor: string | null) {
  const knowledgeBaseId = props.knowledgeBaseId
  const token = requestFence.begin('nodes')
  nodesLoading.value = true
  try {
    const page = readPayload(await citationProfileClient.listNodes(knowledgeBaseId, {
      cursor,
      page_size: status.value?.limits.list_page_size || 100,
    }))
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    nodes.value = cursor ? [...nodes.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) nodesLoading.value = false
  }
}

async function setEnrollment(enabled: boolean) {
  const knowledgeBaseId = props.knowledgeBaseId
  const expectedReadVersion = status.value?.snapshot?.read_version || '0'
  const token = requestFence.begin('enrollment')
  enrolling.value = true
  try {
    await citationProfileClient.updateEnrollment(knowledgeBaseId, {
      enabled,
      expected_read_version: expectedReadVersion,
      idempotency_key: makeCitationProfileIdempotencyKey(),
    })
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    await loadAll()
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      MessagePlugin.error(error?.message || '开启状态更新失败。')
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) enrolling.value = false
  }
}

async function openEvidence(node: CitationProfileNode) {
  selectedNode.value = node
  drawerVisible.value = true
  evidence.value = null
  await loadEvidencePage(node.page_uuid, null)
}

async function loadEvidencePage(pageUuid: string, cursor: string | null) {
  const knowledgeBaseId = props.knowledgeBaseId
  const token = requestFence.begin('evidence')
  evidenceLoading.value = true
  try {
    const page = readPayload(await citationProfileClient.getEvidence(knowledgeBaseId, pageUuid, {
      cursor,
      page_size: status.value?.limits.list_page_size || 100,
    }))
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    evidence.value = evidence.value && cursor
      ? { ...page, items: [...evidence.value.items, ...page.items] }
      : page
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      MessagePlugin.error(error?.message || '证据加载失败。')
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) evidenceLoading.value = false
  }
}

async function correct(item: CitationProfileEvidenceItem, action: CitationProfileCorrectionAction) {
  if (!evidence.value || !evidence.value.snapshot || isActionBlocked.value) return
  const knowledgeBaseId = props.knowledgeBaseId
  const token = requestFence.begin('correction')
  const pageUuid = item.page_uuid
  const selectedNodeBeforeReload = selectedNode.value
  const reopenDrawer = drawerVisible.value
  try {
    const result = readPayload(await citationProfileClient.createCorrection(knowledgeBaseId, {
      expected_read_version: evidence.value.snapshot.read_version,
      idempotency_key: makeCitationProfileIdempotencyKey(),
      action,
      event_id: item.event_id,
      page_uuid: pageUuid,
      reason_code: reasonCode.value,
    }))
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    if (status.value?.snapshot) status.value.snapshot.read_version = result.snapshot.read_version
    MessagePlugin.success('纠正已记录。')
    const reloadToken = await loadAll()
    if (reloadToken && requestFence.isCurrent(reloadToken, props.knowledgeBaseId) && reopenDrawer) {
      selectedNode.value = nodes.value.find((node) => node.page_uuid === pageUuid) || selectedNodeBeforeReload
      drawerVisible.value = true
      await loadEvidencePage(pageUuid, null)
    }
  } catch (error: any) {
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    if (error?.code === 'profile_changed' || error?.status === 409) {
      MessagePlugin.warning('证据已变化，请刷新后再确认。')
      return
    }
    MessagePlugin.error(error?.message || '纠正失败。')
  }
}

async function startExport() {
  const knowledgeBaseId = props.knowledgeBaseId
  const expectedReadVersion = currentReadVersion.value || '0'
  const token = requestFence.begin('export')
  exporting.value = true
  try {
    const result = readPayload(await citationProfileClient.createExport(knowledgeBaseId, {
      expected_read_version: expectedReadVersion,
      idempotency_key: makeCitationProfileIdempotencyKey(),
      format: 'json',
    }))
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    operationReceipt.value = result.status === 'ready'
      ? '导出已准备好，请重新授权下载。'
      : '导出正在准备，将在一小时后过期。'
    if (result.status === 'ready') {
      const blob = await citationProfileClient.downloadExport(knowledgeBaseId, result.operation_id)
      if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
      saveExportBlob(blob, result.operation_id)
      operationReceipt.value = '导出已下载。'
    }
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      MessagePlugin.error(error?.message || '导出失败。')
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) exporting.value = false
  }
}

async function deleteCurrent() {
  const knowledgeBaseId = props.knowledgeBaseId
  const expectedReadVersion = currentReadVersion.value || '0'
  const token = requestFence.begin('delete')
  deleting.value = true
  try {
    await citationProfileClient.deleteCurrentProfile(knowledgeBaseId, {
      expected_read_version: expectedReadVersion,
      idempotency_key: makeCitationProfileIdempotencyKey(),
    })
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    const reloadToken = await loadAll()
    if (reloadToken && requestFence.isCurrent(reloadToken, props.knowledgeBaseId)) {
      operationReceipt.value = '画像已停用并从界面隐藏；审计记录按系统保留策略处理。'
    }
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      MessagePlugin.error(error?.message || '删除请求失败。')
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) deleting.value = false
  }
}

async function blindDelete() {
  const knowledgeBaseId = props.knowledgeBaseId
  const token = requestFence.begin('blind-delete')
  blindDeleting.value = true
  try {
    await citationProfileClient.blindDeleteScope(knowledgeBaseId)
    if (!requestFence.isCurrent(token, props.knowledgeBaseId)) return
    // Blind deletion deliberately does not re-read status: doing so would turn
    // this existence-hiding endpoint into an oracle. Fence every in-flight read
    // and remove every retained snapshot/evidence reference from component
    // memory before acknowledging the request.
    requestFence.reset(knowledgeBaseId)
    resetPanelState()
    operationReceipt.value = '请求已受理。'
  } catch (error: any) {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) {
      MessagePlugin.error(error?.message || '请求失败。')
    }
  } finally {
    if (requestFence.isCurrent(token, props.knowledgeBaseId)) blindDeleting.value = false
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

function correctionStateLabel(state: CitationProfileEvidenceItem['correction_state']): string {
  switch (state) {
    case 'confirmed':
      return '已确认'
    case 'rejected':
      return '已拒绝'
    case 'event_retracted':
      return '已撤回'
    default:
      return '未纠正'
  }
}

function overlayLabel(overlay: CitationProfileNode['overlay']): string {
  switch (overlay) {
    case 'evidenced_current':
      return '当前版本有来源证据'
    case 'evidenced_historical':
      return '历史版本来源证据'
    case 'disputed':
      return '你已质疑此映射'
    default:
      return '暂无引用证据'
  }
}

function overlayTheme(overlay: CitationProfileNode['overlay']) {
  if (overlay === 'disputed') return 'warning'
  if (overlay === 'unknown') return 'default'
  return 'success'
}

watch(() => props.knowledgeBaseId, () => {
  void loadAll()
}, { immediate: true })
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
    min-width: 0;
    height: auto;
    box-sizing: border-box;
    border-left: 0;
    border-top: 1px solid var(--td-component-stroke);
  }
}
</style>
