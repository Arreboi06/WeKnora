<template>
  <t-drawer
    v-model:visible="internalVisible"
    class="workbench-drawer"
    placement="right"
    size="520px"
    attach="body"
    :footer="false"
    :destroy-on-close="false"
    :close-on-overlay-click="true"
    :close-on-esc-keydown="true"
  >
    <template #header>
      <div class="workbench-drawer__header">
        <span class="workbench-drawer__header-icon"><t-icon name="terminal" /></span>
        <span class="workbench-drawer__title">Workbench</span>
      </div>
    </template>

    <div v-if="!available" class="workbench-drawer__empty">
      <t-icon name="lock-on" size="30px" />
      <span>Workbench unavailable</span>
    </div>

    <div v-else class="workbench-drawer__body">
      <section class="workbench-drawer__section">
        <div class="workbench-drawer__row">
          <div class="workbench-drawer__status">
            <span class="workbench-drawer__label">Session</span>
            <strong>{{ workbench?.state || 'not started' }}</strong>
          </div>
          <t-button theme="primary" size="small" :loading="starting" @click="ensureStarted">
            <template #icon><t-icon name="play-circle" /></template>
            {{ workbench ? 'Ready' : 'Start' }}
          </t-button>
        </div>
        <div class="workbench-drawer__meta">
          <span>{{ selectedBackend }}</span>
          <span v-if="workbench">epoch {{ workbench.lease_epoch }}</span>
          <span v-if="job">job {{ shortId(job.id) }}</span>
        </div>
      </section>

      <section class="workbench-drawer__section">
        <label class="workbench-drawer__label" for="workbench-command">Command</label>
        <t-textarea
          id="workbench-command"
          v-model="commandDraft"
          :autosize="{ minRows: 3, maxRows: 6 }"
          placeholder="echo ok"
        />
        <t-checkbox
          id="workbench-presentation-skill"
          v-model="presentationSkillMode"
          :disabled="running"
          class="workbench-drawer__skill-mode"
        >
          Presentation Skill candidate
        </t-checkbox>
        <div class="workbench-drawer__actions">
          <t-button theme="primary" :loading="running" :disabled="!canRun" @click="runCommand">
            <template #icon><t-icon name="play" /></template>
            Run
          </t-button>
          <t-button variant="text" :disabled="!job" @click="refreshTimeline">
            <template #icon><t-icon name="refresh" /></template>
          </t-button>
        </div>
      </section>

      <section v-if="commandResult" class="workbench-drawer__section">
        <div class="workbench-drawer__result-head">
          <span class="workbench-drawer__label">Result</span>
          <t-tag size="small" :theme="commandResult.exit_code === 0 ? 'success' : 'danger'" variant="light">
            exit {{ commandResult.exit_code ?? 'n/a' }}
          </t-tag>
        </div>
        <pre v-if="commandResult.stdout" class="workbench-drawer__output">{{ commandResult.stdout }}</pre>
        <pre v-if="commandResult.stderr" class="workbench-drawer__output is-error">{{ commandResult.stderr }}</pre>
      </section>

      <section class="workbench-drawer__section">
        <WorkbenchFilesPanel
          :session-id="sessionId"
          :available="available"
          :workbench="workbench"
          :job="job"
          :last-command-id="lastCommandId"
          :ensure-job="ensureJob"
          :initial-root="initialFileRoot"
        />
      </section>

      <section class="workbench-drawer__section">
        <div class="workbench-drawer__result-head">
          <span class="workbench-drawer__label">Events</span>
          <t-button variant="text" size="small" :disabled="!job || refreshing" :loading="refreshing" @click="refreshTimeline">
            <template #icon><t-icon name="refresh" /></template>
          </t-button>
        </div>
        <div v-if="!events.length" class="workbench-drawer__empty-line">No events</div>
        <ul v-else class="workbench-drawer__list">
          <li v-for="event in events" :key="event.id">
            <span>{{ event.seq }}</span>
            <strong>{{ event.event_type }}</strong>
          </li>
        </ul>
      </section>

      <section class="workbench-drawer__section">
        <span class="workbench-drawer__label">Audit</span>
        <div v-if="!auditRows.length" class="workbench-drawer__empty-line">No audit rows</div>
        <ul v-else class="workbench-drawer__list">
          <li v-for="row in auditRows" :key="row.id">
            <span>{{ row.state || 'PENDING' }}</span>
            <strong>{{ row.action }}</strong>
          </li>
        </ul>
      </section>
    </div>
  </t-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import WorkbenchFilesPanel from './WorkbenchFilesPanel.vue'
import type { WorkbenchFileRoot } from './workbenchFiles'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  createWorkbenchCommand,
  createWorkbenchJob,
  getActiveWorkbench,
  getWorkbenchJob,
  listWorkbenchAudit,
  listWorkbenchEvents,
  startWorkbench,
  type WorkbenchAuditOutboxResponse,
  type WorkbenchCommandResponse,
  type WorkbenchJobResponse,
  type WorkbenchRunnerEventResponse,
  type WorkbenchSessionResponse,
} from '@/api/workbench'
import {
  buildWorkbenchCommandPayload,
  buildWorkbenchJobPayload,
  buildWorkbenchStartPayload,
  WORKBENCH_PRESENTATION_SKILL_NAME,
  WORKBENCH_PRESENTATION_SKILL_OPERATION,
  normalizeEligibleBackends,
  randomWorkbenchIncarnationID,
  randomWorkbenchToken,
  sanitizeWorkbenchAuditRows,
  shouldShowWorkbenchEntry,
} from './workbenchEntry'

const props = withDefaults(defineProps<{
  visible: boolean
  sessionId: string
  capabilitySupported: boolean
  eligibleBackends?: string[]
  initialFileRoot?: WorkbenchFileRoot
}>(), {
  eligibleBackends: () => [],
  initialFileRoot: 'workspace',
})

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
}>()

const internalVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const workbench = ref<WorkbenchSessionResponse | null>(null)
const job = ref<WorkbenchJobResponse | null>(null)
const commandDraft = ref('echo ok')
const commandResult = ref<WorkbenchCommandResponse | null>(null)
const presentationSkillMode = ref(false)
const lastCommandId = ref('')
const events = ref<WorkbenchRunnerEventResponse[]>([])
const auditRows = ref<WorkbenchAuditOutboxResponse[]>([])
const starting = ref(false)
const running = ref(false)
const refreshing = ref(false)
const incarnationId = ref(randomWorkbenchIncarnationID())
const sequence = ref(1)

const available = computed(() => shouldShowWorkbenchEntry({
  sessionId: props.sessionId,
  capabilitySupported: props.capabilitySupported,
  eligibleBackends: props.eligibleBackends,
}))
const selectedBackend = computed(() => normalizeEligibleBackends(props.eligibleBackends)[0] || '')
const canRun = computed(() => available.value && commandDraft.value.trim().length > 0 && !running.value)

watch(() => props.visible, (open) => {
  if (open && available.value) void hydrateExistingWorkbench()
  if (!open) commandResult.value = null
})

async function hydrateExistingWorkbench(): Promise<void> {
  if (!props.sessionId) return
  try {
    const response = await getActiveWorkbench(props.sessionId)
    workbench.value = response.data
    incarnationId.value = response.data.incarnation_id || incarnationId.value
  } catch (err) {
    if (!isNotFound(err)) {
      MessagePlugin.error(errorMessage(err, 'Workbench unavailable'))
    }
  }
}

async function ensureStarted(): Promise<WorkbenchSessionResponse | null> {
  if (!available.value || !props.sessionId) return null
  if (workbench.value?.state === 'READY') return workbench.value
  starting.value = true
  try {
    await hydrateExistingWorkbench()
    if (workbench.value?.state === 'READY') return workbench.value
    const response = await startWorkbench(props.sessionId, buildWorkbenchStartPayload({
      incarnationId: incarnationId.value,
      sandboxConfigId: selectedBackend.value,
      eligibleBackends: props.eligibleBackends,
    }))
    workbench.value = response.data
    return response.data
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Workbench start failed'))
    return null
  } finally {
    starting.value = false
  }
}

async function ensureJob(): Promise<WorkbenchJobResponse | null> {
  const wb = await ensureStarted()
  if (!wb || !props.sessionId) return null
  if (job.value && job.value.state === 'RUNNING') return job.value
  const response = await createWorkbenchJob(props.sessionId, buildWorkbenchJobPayload({
    workbenchId: wb.id,
    leaseEpoch: wb.lease_epoch,
    startNonce: randomWorkbenchToken('nonce'),
    backend: selectedBackend.value,
  }))
  job.value = response.data
  return response.data
}

async function runCommand(): Promise<void> {
  if (!canRun.value || !props.sessionId) return
  running.value = true
  lastCommandId.value = ''
  try {
    const currentJob = await ensureJob()
    if (!currentJob) return
    const response = await createWorkbenchCommand(
      props.sessionId,
      currentJob.id,
      buildWorkbenchCommandPayload({
        command: commandDraft.value,
        sequence: sequence.value,
        leaseEpoch: currentJob.lease_epoch,
        stateVersion: currentJob.state_version,
        skillName: presentationSkillMode.value ? WORKBENCH_PRESENTATION_SKILL_NAME : undefined,
        skillOperation: presentationSkillMode.value ? WORKBENCH_PRESENTATION_SKILL_OPERATION : undefined,
      }),
    )
    commandResult.value = response.data
    if (response.data?.state === 'SUCCEEDED' && response.data.command_id) lastCommandId.value = response.data.command_id
    sequence.value += 1
    await refreshJob(currentJob.id)
    await refreshTimeline()
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Command failed'))
  } finally {
    running.value = false
  }
}

async function refreshJob(jobId: string): Promise<void> {
  if (!props.sessionId || !jobId) return
  try {
    const response = await getWorkbenchJob(props.sessionId, jobId)
    job.value = response.data
  } catch {
    // The command result is already visible; timeline refresh will surface later failures.
  }
}

async function refreshTimeline(): Promise<void> {
  if (!props.sessionId || !job.value || refreshing.value) return
  refreshing.value = true
  try {
    const [eventResponse, auditResponse] = await Promise.all([
      listWorkbenchEvents(props.sessionId, { jobId: job.value.id, limit: 50 }),
      listWorkbenchAudit(props.sessionId, { jobId: job.value.id, limit: 50 }),
    ])
    events.value = eventResponse.data?.events || []
    auditRows.value = sanitizeWorkbenchAuditRows(auditResponse.data?.audit || [])
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Refresh failed'))
  } finally {
    refreshing.value = false
  }
}

function shortId(id: string): string {
  return id.length <= 8 ? id : id.slice(0, 8)
}

function isNotFound(err: unknown): boolean {
  const detail = err as { status?: number; code?: string }
  return detail?.status === 404 || detail?.code === 'workbench_not_found'
}

function errorMessage(err: unknown, fallback: string): string {
  if (typeof err === 'object' && err !== null) {
    const message = (err as { message?: unknown }).message
    if (typeof message === 'string' && message.trim()) return message
  }
  return fallback
}
</script>

<style scoped lang="less">
.workbench-drawer__header {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.workbench-drawer__header-icon {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--td-brand-color);
  background: var(--td-brand-color-light);
}

.workbench-drawer__title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 15px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.workbench-drawer__body {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.workbench-drawer__section {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.workbench-drawer__section:last-child {
  border-bottom: 0;
}

.workbench-drawer__row,
.workbench-drawer__result-head,
.workbench-drawer__actions,
.workbench-drawer__meta {
  display: flex;
  align-items: center;
}

.workbench-drawer__row,
.workbench-drawer__result-head {
  justify-content: space-between;
  gap: 12px;
}

.workbench-drawer__actions {
  justify-content: flex-end;
  gap: 8px;
}

.workbench-drawer__status {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.workbench-drawer__status strong {
  color: var(--td-text-color-primary);
  font-size: 14px;
  line-height: 20px;
}

.workbench-drawer__label {
  color: var(--td-text-color-secondary);
  font-size: 12px;
  font-weight: 600;
  line-height: 18px;
}

.workbench-drawer__meta {
  flex-wrap: wrap;
  gap: 8px;
  color: var(--td-text-color-placeholder);
  font-size: 12px;
  line-height: 18px;
}

.workbench-drawer__output {
  max-height: 220px;
  margin: 0;
  padding: 10px 12px;
  border-radius: 8px;
  overflow: auto;
  color: var(--td-text-color-primary);
  background: var(--td-bg-color-secondarycontainer);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

.workbench-drawer__output.is-error {
  color: var(--td-error-color-7);
  background: var(--td-error-color-1);
}

.workbench-drawer__list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.workbench-drawer__list li {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 18px;
}

.workbench-drawer__list strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--td-text-color-primary);
  font-size: 13px;
}

.workbench-drawer__empty,
.workbench-drawer__empty-line {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 96px;
  color: var(--td-text-color-placeholder);
  font-size: 13px;
}

.workbench-drawer__empty-line {
  min-height: 32px;
  justify-content: flex-start;
}
</style>

<style lang="less">
.workbench-drawer.t-drawer {
  .t-drawer__header {
    padding: 16px 20px;
    font-weight: normal;
  }

  .t-drawer__body {
    padding: 4px 20px 18px;
  }
}
</style>
