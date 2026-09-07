import { get, getDown, post } from '@/utils/request'
import type {
  WorkbenchCommandPayload,
  WorkbenchJobPayload,
  WorkbenchStartPayload,
} from '@/components/workbench/workbenchEntry'

import type {
  WorkbenchFileEntry,
  WorkbenchFileRef,
} from '@/components/workbench/workbenchFiles'

export interface WorkbenchSessionResponse {
  id: string
  chat_session_id: string
  incarnation_id: string
  backend_type: string
  state: string
  state_version: number
  lease_epoch: number
  stream_ticket_only?: boolean
}

export interface WorkbenchJobResponse {
  id: string
  workbench_id: string
  chat_session_id: string
  backend_type: string
  state: string
  state_version: number
  lease_epoch: number
  created_at?: string
  resource_policy?: Record<string, unknown>
}

export interface WorkbenchCommandResponse {
  command_id: string
  state: string
  stdout?: string
  stderr?: string
  exit_code?: number
  killed?: boolean
}

export interface WorkbenchRunnerEventResponse {
  id: string
  job_id: string
  command_id?: string
  seq: number
  event_type: string
  payload?: Record<string, unknown>
  created_at?: string
}

export interface WorkbenchAuditOutboxResponse {
  id: string
  job_id: string
  command_id?: string
  action: string
  actor_user_id?: string
  outcome?: string
  payload?: Record<string, unknown>
  state?: string
  attempts?: number
  created_at?: string
  updated_at?: string
  published_at?: string | null
}

export interface WorkbenchEnvelope<T> {
  success: boolean
  data: T
  message?: string
}

export interface WorkbenchFileScopePayload {
  workbench_id: string
  job_id: string
  expected_lease_epoch: number
}

export interface WorkbenchArtifactResponse {
  artifact_id: string
  version: number
  chat_session_id: string
  workbench_id: string
  job_id: string
  command_id?: string
  skill_run_id?: string
  source_file_ref: WorkbenchFileRef
  content_sha256: string
  size_bytes: number
  mime_type: string
  preview_class: string
  file_name: string
  created_by?: string
  created_at?: string
  download_endpoint: string
  preview_endpoint: string
}

export interface WorkbenchSkillRunResponse {
  skill_run_id: string
  skill_name: string
  skill_operation: string
  state: string
  state_version: number
  chat_session_id: string
  workbench_id: string
  job_id: string
  command_id?: string
  output_file_ref?: WorkbenchFileRef
  output_artifact_id?: string
  output_artifact_version?: number
  artifact?: WorkbenchArtifactResponse
}

export interface WorkbenchFileScopeResponse {
  entries?: WorkbenchFileEntry[]
  entry?: WorkbenchFileEntry
  content_b64?: string
  deleted?: boolean
}

export function browseWorkbenchFiles(
  sessionId: string,
  scope: WorkbenchFileScopePayload,
  ref: WorkbenchFileRef,
): Promise<WorkbenchEnvelope<{ entries: WorkbenchFileEntry[] }>> {
  return post(sessionWorkbenchPath(sessionId, '/files/browse'), { ...scope, ref }) as unknown as Promise<WorkbenchEnvelope<{ entries: WorkbenchFileEntry[] }>>
}

export function uploadWorkbenchFile(
  sessionId: string,
  scope: WorkbenchFileScopePayload,
  ref: WorkbenchFileRef,
  contentB64: string,
): Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/files/upload'), { ...scope, ref, content_b64: contentB64 }) as unknown as Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>>
}

export function downloadWorkbenchFile(
  sessionId: string,
  scope: WorkbenchFileScopePayload,
  ref: WorkbenchFileRef,
): Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/files/download'), { ...scope, ref }) as unknown as Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>>
}

export function renameWorkbenchFile(
  sessionId: string,
  scope: WorkbenchFileScopePayload,
  source: WorkbenchFileRef,
  target: WorkbenchFileRef,
): Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/files/rename'), { ...scope, source, target }) as unknown as Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>>
}

export function deleteWorkbenchFile(
  sessionId: string,
  scope: WorkbenchFileScopePayload,
  ref: WorkbenchFileRef,
): Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/files/delete'), { ...scope, ref }) as unknown as Promise<WorkbenchEnvelope<WorkbenchFileScopeResponse>>
}

export function publishWorkbenchArtifact(
  sessionId: string,
  payload: WorkbenchFileScopePayload & {
    source_ref: WorkbenchFileRef
    artifact_id?: string
    version?: number
    command_id: string
  },
): Promise<WorkbenchEnvelope<WorkbenchArtifactResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/artifacts'), payload) as unknown as Promise<WorkbenchEnvelope<WorkbenchArtifactResponse>>
}

export function getWorkbenchArtifact(
  sessionId: string,
  artifactId: string,
  version: number,
): Promise<WorkbenchEnvelope<WorkbenchArtifactResponse>> {
  return get(workbenchArtifactPath(sessionId, artifactId, version)) as unknown as Promise<WorkbenchEnvelope<WorkbenchArtifactResponse>>
}

export function downloadWorkbenchArtifact(
  sessionId: string,
  artifactId: string,
  version: number,
): Promise<Blob> {
  return getDown(workbenchArtifactPath(sessionId, artifactId, version) + '/download')
}

export function previewWorkbenchArtifact(
  sessionId: string,
  artifactId: string,
  version: number,
): Promise<string> {
  return get<string>(workbenchArtifactPath(sessionId, artifactId, version) + '/preview', { responseType: 'text' })
}

export function publishWorkbenchPresentationSkill(
  sessionId: string,
  payload: WorkbenchFileScopePayload & {
    source_ref: WorkbenchFileRef
    artifact_id?: string
    version?: number
    command_id: string
  },
): Promise<WorkbenchEnvelope<WorkbenchSkillRunResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/skill-runs/presentation'), payload) as unknown as Promise<WorkbenchEnvelope<WorkbenchSkillRunResponse>>
}

export function startWorkbench(
  sessionId: string,
  payload: WorkbenchStartPayload,
): Promise<WorkbenchEnvelope<WorkbenchSessionResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/start'), payload) as unknown as Promise<WorkbenchEnvelope<WorkbenchSessionResponse>>
}

export function getActiveWorkbench(
  sessionId: string,
): Promise<WorkbenchEnvelope<WorkbenchSessionResponse>> {
  return get(sessionWorkbenchPath(sessionId, '')) as unknown as Promise<WorkbenchEnvelope<WorkbenchSessionResponse>>
}

export function createWorkbenchJob(
  sessionId: string,
  payload: WorkbenchJobPayload,
): Promise<WorkbenchEnvelope<WorkbenchJobResponse>> {
  return post(sessionWorkbenchPath(sessionId, '/jobs'), payload) as unknown as Promise<WorkbenchEnvelope<WorkbenchJobResponse>>
}

export function getWorkbenchJob(
  sessionId: string,
  jobId: string,
): Promise<WorkbenchEnvelope<WorkbenchJobResponse>> {
  return get(`${sessionWorkbenchPath(sessionId, '/jobs')}/${encodeURIComponent(jobId)}`) as unknown as Promise<WorkbenchEnvelope<WorkbenchJobResponse>>
}

export function createWorkbenchCommand(
  sessionId: string,
  jobId: string,
  payload: WorkbenchCommandPayload,
): Promise<WorkbenchEnvelope<WorkbenchCommandResponse>> {
  return post(
    `${sessionWorkbenchPath(sessionId, '/jobs')}/${encodeURIComponent(jobId)}/commands`,
    payload,
  ) as unknown as Promise<WorkbenchEnvelope<WorkbenchCommandResponse>>
}

export function listWorkbenchEvents(
  sessionId: string,
  params: { jobId: string; afterSeq?: number; limit?: number },
): Promise<WorkbenchEnvelope<{ events: WorkbenchRunnerEventResponse[] }>> {
  const qs = new URLSearchParams({ job_id: params.jobId })
  if (params.afterSeq && params.afterSeq > 0) qs.set('after_seq', String(params.afterSeq))
  if (params.limit && params.limit > 0) qs.set('limit', String(params.limit))
  return get(`${sessionWorkbenchPath(sessionId, '/events')}?${qs.toString()}`) as unknown as Promise<WorkbenchEnvelope<{ events: WorkbenchRunnerEventResponse[] }>>
}

export function listWorkbenchAudit(
  sessionId: string,
  params: { jobId: string; state?: string; limit?: number },
): Promise<WorkbenchEnvelope<{ audit: WorkbenchAuditOutboxResponse[] }>> {
  const qs = new URLSearchParams({ job_id: params.jobId })
  if (params.state) qs.set('state', params.state)
  if (params.limit && params.limit > 0) qs.set('limit', String(params.limit))
  return get(`${sessionWorkbenchPath(sessionId, '/audit')}?${qs.toString()}`) as unknown as Promise<WorkbenchEnvelope<{ audit: WorkbenchAuditOutboxResponse[] }>>
}

function workbenchArtifactPath(sessionId: string, artifactId: string, version: number): string {
  return sessionWorkbenchPath(sessionId, '') + '/artifacts/' + encodeURIComponent(artifactId) + '/versions/' + encodeURIComponent(String(version))
}

function sessionWorkbenchPath(sessionId: string, suffix: string): string {
  return `/api/v1/sessions/${encodeURIComponent(sessionId)}/workbench${suffix}`
}
