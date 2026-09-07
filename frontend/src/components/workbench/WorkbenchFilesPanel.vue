<template>
  <section class="workbench-files">
    <div class="workbench-files__header">
      <div>
        <span class="workbench-files__label">Files and outputs</span>
        <strong>{{ currentPath }}</strong>
      </div>
      <div class="workbench-files__actions">
        <t-button variant="text" size="small" title="Refresh files" :loading="loading" :disabled="!available" @click="refresh">
          <template #icon><t-icon name="refresh" /></template>
        </t-button>
        <t-button variant="text" size="small" title="Upload file" :disabled="!canUpload" @click="openUpload">
          <template #icon><t-icon name="upload" /></template>
        </t-button>
        <input ref="fileInput" class="workbench-files__input" type="file" @change="handleUpload" />
      </div>
    </div>

    <div class="workbench-files__toolbar">
      <t-select v-model="activeRoot" size="small" :options="rootOptions" />
      <t-button variant="text" size="small" :disabled="!canGoUp || loading" title="Go to parent" @click="goUp">
        <template #icon><t-icon name="chevron-left" /></template>
      </t-button>
      <t-button variant="text" size="small" :disabled="!selectedFile || loading" title="Download selected file" @click="downloadFile">
        <template #icon><t-icon name="download" /></template>
      </t-button>
      <t-button variant="text" size="small" :disabled="!canRename || loading" title="Rename selected file" @click="beginRename">
        <template #icon><t-icon name="edit" /></template>
      </t-button>
      <t-button variant="text" size="small" theme="danger" :disabled="!selectedFile || loading" title="Delete selected file" @click="deleteFile">
        <template #icon><t-icon name="delete" /></template>
      </t-button>
    </div>

    <div v-if="renaming" class="workbench-files__rename">
      <t-input v-model="renameValue" size="small" autofocus @enter="commitRename" />
      <t-button size="small" theme="primary" :loading="loading" @click="commitRename">Save</t-button>
      <t-button size="small" variant="text" :disabled="loading" @click="cancelRename">Cancel</t-button>
    </div>

    <div v-if="!available" class="workbench-files__empty">Workbench is unavailable.</div>
    <div v-else-if="!entries.length && !loading" class="workbench-files__empty">No files in this location.</div>
    <ul v-else class="workbench-files__list">
      <li v-for="entry in entries" :key="entryKey(entry)" :class="{ 'is-selected': entryKey(entry) === selectedKey }">
        <button type="button" class="workbench-files__entry" @click="selectEntry(entry)" @dblclick="openEntry(entry)">
          <t-icon :name="isDirectory(entry) ? 'folder-open' : 'file-paste'" />
          <span class="workbench-files__name">{{ entry.name }}</span>
          <span class="workbench-files__size">{{ isDirectory(entry) ? 'folder' : formatWorkbenchBytes(entry.size_bytes) }}</span>
        </button>
        <div v-if="entryKey(entry) === selectedKey && canPublishWorkbenchArtifact(entry, lastCommandId)" class="workbench-files__entry-actions">
          <t-button variant="text" size="small" title="Publish immutable artifact" :loading="publishing" @click.stop="publishArtifact(entry)">
            <template #icon><t-icon name="save" /></template>
          </t-button>
          <t-button v-if="canPublishPresentationCandidate(entry, lastCommandId)" variant="text" size="small" title="Run Presentation Skill candidate" :loading="publishingSkill" @click.stop="publishPresentation(entry)">
            <template #icon><t-icon name="presentation" /></template>
          </t-button>
        </div>
      </li>
    </ul>

    <div v-if="artifacts.length" class="workbench-files__artifacts">
      <div class="workbench-files__label">Immutable artifacts</div>
      <ul class="workbench-files__list">
        <li v-for="artifact in artifacts" :key="artifact.artifact_id + ':' + artifact.version">
          <div class="workbench-files__artifact">
            <t-icon name="file-copy" />
            <span class="workbench-files__name">{{ artifact.file_name }}</span>
            <t-tag size="small" variant="light">{{ artifact.preview_class }}</t-tag>
            <span class="workbench-files__size">v{{ artifact.version }}</span>
          </div>
          <div class="workbench-files__entry-actions">
            <t-button v-if="previewSandboxMode(artifact.preview_class) !== null" variant="text" size="small" title="Open isolated preview" :loading="previewing === artifact.artifact_id" @click="openPreview(artifact)">
              <template #icon><t-icon name="browse" /></template>
            </t-button>
            <t-button variant="text" size="small" title="Download artifact" @click="downloadArtifact(artifact)">
              <template #icon><t-icon name="download" /></template>
            </t-button>
          </div>
        </li>
      </ul>
    </div>

    <div v-if="previewUrl" class="workbench-files__preview">
      <div class="workbench-files__preview-header">
        <span class="workbench-files__label">Isolated preview</span>
        <t-button variant="text" size="small" title="Close preview" @click="closePreview">
          <template #icon><t-icon name="close" /></template>
        </t-button>
      </div>
      <iframe
        class="workbench-files__frame"
        :src="previewUrl"
        :sandbox="previewSandbox === null ? undefined : previewSandbox"
        :title="previewTitle"
        referrerpolicy="no-referrer"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import {
  browseWorkbenchFiles,
  deleteWorkbenchFile,
  downloadWorkbenchArtifact,
  downloadWorkbenchFile,
  publishWorkbenchArtifact,
  publishWorkbenchPresentationSkill,
  renameWorkbenchFile,
  previewWorkbenchArtifact,
  uploadWorkbenchFile,
  type WorkbenchArtifactResponse,
  type WorkbenchFileScopePayload,
  type WorkbenchJobResponse,
  type WorkbenchSessionResponse,
} from '@/api/workbench'
import {
  canPublishPresentationCandidate,
  canPublishWorkbenchArtifact,
  childWorkbenchFileRef,
  hardenWorkbenchPreviewHTML,
  formatWorkbenchBytes,
  previewSandboxMode,
  rootWorkbenchFileRef,
  type WorkbenchFileEntry,
  type WorkbenchFileRef,
  type WorkbenchFileRoot,
} from './workbenchFiles'

const props = withDefaults(defineProps<{
  sessionId: string
  available: boolean
  workbench: WorkbenchSessionResponse | null
  job: WorkbenchJobResponse | null
  lastCommandId: string
  ensureJob: () => Promise<WorkbenchJobResponse | null>
  initialRoot?: WorkbenchFileRoot
}>(), {
  lastCommandId: '',
  initialRoot: 'workspace',
})

const activeRoot = ref<WorkbenchFileRoot>(props.initialRoot)
const currentRef = ref<WorkbenchFileRef>(rootWorkbenchFileRef(props.initialRoot))
const entries = ref<WorkbenchFileEntry[]>([])
const selectedKey = ref('')
const renameValue = ref('')
const renaming = ref(false)
const loading = ref(false)
const publishing = ref(false)
const publishingSkill = ref(false)
const previewing = ref('')
const artifacts = ref<WorkbenchArtifactResponse[]>([])
const previewUrl = ref('')
const previewTitle = ref('')
const previewSandbox = ref<'allow-scripts' | '' | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const rootOptions = [
  { label: 'Workspace', value: 'workspace' },
  { label: 'Input', value: 'input' },
  { label: 'Output', value: 'output' },
]

const selectedFile = computed(() => entries.value.find((entry) => entryKey(entry) === selectedKey.value) || null)
const availableScope = computed(() => buildScope(props.job))
const currentPath = computed(() => '/' + currentRef.value.root + (currentRef.value.segments.length ? '/' + currentRef.value.segments.join('/') : ''))
const canGoUp = computed(() => currentRef.value.segments.length > 0)
const canUpload = computed(() => props.available && activeRoot.value !== 'output')
const canRename = computed(() => Boolean(selectedFile.value && selectedFile.value.type === 'file'))


watch(activeRoot, (root) => {
  currentRef.value = rootWorkbenchFileRef(root)
  selectedKey.value = ''
  renaming.value = false
  void refresh()
})

watch(() => props.job?.id, () => {
  if (props.available && props.job) void refresh()
})

watch(() => props.available, (available) => {
  if (available && props.job) void refresh()
})


watch(() => props.lastCommandId, (commandId) => {
  if (commandId && props.available) void refresh()
})
async function refresh(): Promise<void> {
  const scope = await ensureScope()
  if (!scope) {
    entries.value = []
    return
  }
  loading.value = true
  try {
    const response = await browseWorkbenchFiles(props.sessionId, scope, currentRef.value)
    entries.value = Array.isArray(response.data?.entries) ? response.data.entries : []
    if (!entries.value.some((entry) => entryKey(entry) === selectedKey.value)) selectedKey.value = ''
  } catch (err) {
    entries.value = []
    MessagePlugin.error(errorMessage(err, 'File listing failed'))
  } finally {
    loading.value = false
  }
}

function selectEntry(entry: WorkbenchFileEntry): void {
  selectedKey.value = entryKey(entry)
  renameValue.value = entry.name
}

function openEntry(entry: WorkbenchFileEntry): void {
  if (!isDirectory(entry)) return
  currentRef.value = entry.ref
  selectedKey.value = ''
  renaming.value = false
  void refresh()
}

function goUp(): void {
  if (!canGoUp.value) return
  currentRef.value = {
    file_ref_version: 1,
    root: currentRef.value.root,
    segments: currentRef.value.segments.slice(0, -1),
  }
  selectedKey.value = ''
  void refresh()
}

function beginRename(): void {
  if (!selectedFile.value || !canRename.value) return
  renameValue.value = selectedFile.value.name
  renaming.value = true
}

function cancelRename(): void {
  renaming.value = false
  renameValue.value = ''
}

async function commitRename(): Promise<void> {
  const entry = selectedFile.value
  const scope = await ensureScope()
  const targetName = renameValue.value.trim()
  if (!entry || !scope || !targetName || targetName === entry.name) {
    cancelRename()
    return
  }
  const parent: WorkbenchFileRef = {
    file_ref_version: 1,
    root: entry.ref.root,
    segments: entry.ref.segments.slice(0, -1),
  }
  const target = childWorkbenchFileRef(parent, targetName)
  if (!target) {
    MessagePlugin.error('Invalid file name')
    return
  }
  loading.value = true
  try {
    await renameWorkbenchFile(props.sessionId, scope, entry.ref, target)
    cancelRename()
    await refresh()
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Rename failed'))
  } finally {
    loading.value = false
  }
}

async function deleteFile(): Promise<void> {
  const entry = selectedFile.value
  const scope = await ensureScope()
  if (!entry || !scope) return
  const dialog = DialogPlugin.confirm({
    header: 'Delete file',
    body: 'This removes the selected path from the protected workspace.',
    confirmBtn: 'Delete',
    cancelBtn: 'Cancel',
    onConfirm: async () => {
      loading.value = true
      try {
        await deleteWorkbenchFile(props.sessionId, scope, entry.ref)
        selectedKey.value = ''
        await refresh()
        dialog.destroy()
      } catch (err) {
        MessagePlugin.error(errorMessage(err, 'Delete failed'))
        dialog.hide()
      } finally {
        loading.value = false
      }
    },
    onCancel: () => dialog.destroy(),
  })
}

function openUpload(): void {
  if (!canUpload.value) return
  fileInput.value?.click()
}

async function handleUpload(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  const scope = await ensureScope()
  const target = childWorkbenchFileRef(currentRef.value, file.name)
  if (!scope || !target) {
    MessagePlugin.error('Upload path is not allowed')
    return
  }
  if (file.size > 100 * 1024 * 1024) {
    MessagePlugin.error('File exceeds the 100 MiB Workbench limit')
    return
  }
  loading.value = true
  try {
    const content = await file.arrayBuffer()
    await uploadWorkbenchFile(props.sessionId, scope, target, encodeBase64(new Uint8Array(content)))
    await refresh()
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Upload failed'))
  } finally {
    loading.value = false
  }
}

async function downloadFile(): Promise<void> {
  const entry = selectedFile.value
  const scope = await ensureScope()
  if (!entry || entry.type !== 'file' || !scope) return
  loading.value = true
  try {
    const response = await downloadWorkbenchFile(props.sessionId, scope, entry.ref)
    const content = response.data?.content_b64
    if (!content) throw new Error('empty file response')
    triggerDownload(new Blob([decodeBase64(content)], { type: 'application/octet-stream' }), entry.name)
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Download failed'))
  } finally {
    loading.value = false
  }
}

async function publishArtifact(entry: WorkbenchFileEntry): Promise<void> {
  if (!canPublishWorkbenchArtifact(entry, props.lastCommandId)) return
  const scope = await ensureScope()
  if (!scope) return
  publishing.value = true
  try {
    const response = await publishWorkbenchArtifact(props.sessionId, {
      ...scope,
      source_ref: entry.ref,
      command_id: props.lastCommandId,
    })
    addArtifact(response.data)
    MessagePlugin.success('Immutable artifact published')
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Artifact publish failed'))
  } finally {
    publishing.value = false
  }
}

async function publishPresentation(entry: WorkbenchFileEntry): Promise<void> {
  if (!canPublishPresentationCandidate(entry, props.lastCommandId)) return
  const scope = await ensureScope()
  if (!scope) return
  publishingSkill.value = true
  try {
    const response = await publishWorkbenchPresentationSkill(props.sessionId, {
      ...scope,
      source_ref: entry.ref,
      command_id: props.lastCommandId,
    })
    if (response.data?.artifact) addArtifact(response.data.artifact)
    MessagePlugin.success('Presentation candidate published')
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Presentation Skill failed'))
  } finally {
    publishingSkill.value = false
  }
}

async function openPreview(artifact: WorkbenchArtifactResponse): Promise<void> {
  const mode = previewSandboxMode(artifact.preview_class)
  if (mode === null) return
  previewing.value = artifact.artifact_id
  try {
    const html = await previewWorkbenchArtifact(props.sessionId, artifact.artifact_id, artifact.version)
    closePreview()
    const hardenedHTML = hardenWorkbenchPreviewHTML(html, artifact.preview_class)
    previewUrl.value = URL.createObjectURL(new Blob([hardenedHTML], { type: 'text/html;charset=utf-8' }))
    previewTitle.value = artifact.file_name
    previewSandbox.value = mode
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Preview failed'))
  } finally {
    previewing.value = ''
  }
}

function closePreview(): void {
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = ''
  previewTitle.value = ''
  previewSandbox.value = null
}

async function downloadArtifact(artifact: WorkbenchArtifactResponse): Promise<void> {
  try {
    const blob = await downloadWorkbenchArtifact(props.sessionId, artifact.artifact_id, artifact.version)
    triggerDownload(blob, artifact.file_name)
  } catch (err) {
    MessagePlugin.error(errorMessage(err, 'Artifact download failed'))
  }
}

function addArtifact(artifact: WorkbenchArtifactResponse | undefined): void {
  if (!artifact?.artifact_id) return
  artifacts.value = [
    ...artifacts.value.filter((item) => !(item.artifact_id === artifact.artifact_id && item.version === artifact.version)),
    artifact,
  ]
}

function buildScope(job: WorkbenchJobResponse | null): WorkbenchFileScopePayload | null {
  if (!job || !props.workbench || !props.sessionId) return null
  return {
    workbench_id: props.workbench.id,
    job_id: job.id,
    expected_lease_epoch: job.lease_epoch,
  }
}

async function ensureScope(): Promise<WorkbenchFileScopePayload | null> {
  if (!props.available || !props.sessionId || !props.workbench) return null
  if (availableScope.value) return availableScope.value
  const job = await props.ensureJob()
  return buildScope(job)
}

function entryKey(entry: WorkbenchFileEntry): string {
  return JSON.stringify(entry.ref)
}

function isDirectory(entry: WorkbenchFileEntry): boolean {
  return entry.type === 'dir' || entry.type === 'directory'
}

function encodeBase64(bytes: Uint8Array): string {
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + chunkSize, bytes.length)))
  }
  return btoa(binary)
}

function decodeBase64(value: string): ArrayBuffer {
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes.buffer as ArrayBuffer
}

function triggerDownload(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = name.replace(/[\\/:*?"<>|]/g, '_') || 'workbench-file'
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

function errorMessage(err: unknown, fallback: string): string {
  if (typeof err === 'object' && err !== null) {
    const message = (err as { message?: unknown }).message
    if (typeof message === 'string' && message.trim()) return message
  }
  return fallback
}

onUnmounted(closePreview)
</script>

<style scoped lang="less">
.workbench-files {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.workbench-files__header,
.workbench-files__toolbar,
.workbench-files__preview-header,
.workbench-files__artifact,
.workbench-files__entry {
  display: flex;
  align-items: center;
}

.workbench-files__header,
.workbench-files__preview-header {
  justify-content: space-between;
  gap: 12px;
}

.workbench-files__header > div:first-child {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.workbench-files__header strong {
  overflow: hidden;
  color: var(--td-text-color-primary);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workbench-files__label {
  color: var(--td-text-color-secondary);
  font-size: 12px;
  font-weight: 600;
  line-height: 18px;
}

.workbench-files__actions,
.workbench-files__toolbar {
  gap: 4px;
}

.workbench-files__toolbar {
  min-width: 0;
}

.workbench-files__toolbar .t-select {
  flex: 1;
  min-width: 0;
}

.workbench-files__input {
  display: none;
}

.workbench-files__rename {
  display: flex;
  align-items: center;
  gap: 6px;
}

.workbench-files__rename .t-input {
  min-width: 0;
  flex: 1;
}

.workbench-files__list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.workbench-files__list li {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 4px;
  border: 1px solid transparent;
  border-radius: 6px;
}

.workbench-files__list li.is-selected {
  border-color: var(--td-brand-color);
  background: var(--td-brand-color-light);
}

.workbench-files__entry {
  flex: 1;
  min-width: 0;
  gap: 8px;
  padding: 7px 8px;
  border: 0;
  color: var(--td-text-color-primary);
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.workbench-files__entry:hover {
  background: var(--td-bg-color-secondarycontainer);
}

.workbench-files__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workbench-files__size {
  flex: 0 0 auto;
  color: var(--td-text-color-placeholder);
  font-size: 11px;
}

.workbench-files__entry-actions {
  display: flex;
  flex: 0 0 auto;
  gap: 2px;
  padding-right: 4px;
}

.workbench-files__empty {
  min-height: 48px;
  color: var(--td-text-color-placeholder);
  font-size: 12px;
  line-height: 18px;
}

.workbench-files__artifacts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--td-component-stroke);
}

.workbench-files__artifact {
  flex: 1;
  min-width: 0;
  gap: 8px;
  padding: 7px 8px;
}

.workbench-files__artifact .t-tag {
  flex: 0 0 auto;
}

.workbench-files__preview {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--td-component-stroke);
}

.workbench-files__frame {
  width: 100%;
  min-height: 260px;
  border: 1px solid var(--td-component-stroke);
  background: #fff;
}
</style>
