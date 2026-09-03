import { getApiBaseUrl } from '../../utils/api-base'

export type CitationProfileUuid = string
export type CitationProfileToken = string
export type CitationProfileReadVersion = string

export interface CitationProfileGuidance {
  kind: 'none'
  reason: 'evidence_insufficient'
  candidates: []
}

export const CITATION_PROFILE_GUIDANCE: CitationProfileGuidance = {
  kind: 'none',
  reason: 'evidence_insufficient',
  candidates: [],
}

export interface CitationProfileSnapshot {
  subject_epoch?: CitationProfileUuid
  read_version: CitationProfileReadVersion
  event_cutoff?: CitationProfileToken
  correction_cutoff?: CitationProfileToken
  active_run_pointer_cutoff?: CitationProfileToken
  current_index_watermark?: CitationProfileToken
  current_wiki_universe_watermark?: CitationProfileToken
  mapping_revision?: CitationProfileToken
  source_universe_watermark?: CitationProfileToken
  dirty_event_count?: number
  pending_event_count?: number
  pending_mapping_count?: number
  dirty_mapping_count?: number
  captured_at?: string
}

export interface CitationProfileLimits {
  active_scopes: number
  pages: number
  events: number
  links: number
  links_per_event: number
  pending_operations: number
  export_events: number
  graph_nodes: number
  graph_edges: number
  list_page_size: number
  acl_scan_scopes: number
  acl_scan_batch_per_minute: number
}

export type CitationProfileEmptyKind = 'not_enrolled' | 'feature_disabled' | 'no_events' | 'acl_unknown'

export interface CitationProfileEmptyState {
  kind: CitationProfileEmptyKind
  message_code: 'citation_profile_not_enrolled' | 'citation_profile_disabled' | 'citation_profile_no_events' | 'citation_profile_acl_unknown'
}

export interface CitationProfileScope {
  knowledge_base_id: CitationProfileUuid
  subject_epoch: CitationProfileUuid
  profile_policy_version: 'profile_policy_v1'
  retention_policy_version: 'retention_policy_v1'
}

export interface CitationProfileStatusResponse {
  enabled: boolean
  enrolled: boolean
  deleted: boolean
  suspended: boolean
  scope: CitationProfileScope | null
  snapshot: CitationProfileSnapshot | null
  guidance: CitationProfileGuidance
  limits: CitationProfileLimits
  empty_state?: CitationProfileEmptyState
}

export interface CitationProfileEnrollmentRequest {
  enabled: boolean
  expected_read_version?: CitationProfileReadVersion
  idempotency_key: CitationProfileUuid
}

export interface CitationProfileEnrollmentResponse {
  enabled: boolean
  enrolled: boolean
  scope?: CitationProfileScope | null
  snapshot: CitationProfileSnapshot
}

export type CitationProfileOverlay = 'unknown' | 'evidenced_current' | 'evidenced_historical' | 'disputed'

export interface CitationProfileNode {
  page_uuid: CitationProfileUuid
  page_version: CitationProfileToken
  title: string
  slug: string
  page_type: string
  overlay: CitationProfileOverlay
  authorized_evidence_count: number
  current_link_count: number
  historical_link_count: number
  disputed_link_count: number
  stale_mapping: boolean
  evidence_href: string
  updated_at: string
}

export interface CitationProfileNodeListResponse {
  snapshot: CitationProfileSnapshot | null
  items: CitationProfileNode[]
  page_size: number
  next_cursor: string | null
  complete_list: boolean
  guidance: CitationProfileGuidance
  empty_state?: CitationProfileEmptyState
}

export interface CitationProfileGraphEdge {
  source_page_uuid: CitationProfileUuid
  target_page_uuid: CitationProfileUuid
  edge_type: 'wiki_link'
  evidence_event_count: number
  stale_mapping: boolean
}

export interface CitationProfileGraphResponse {
  snapshot: CitationProfileSnapshot | null
  nodes: CitationProfileNode[]
  edges: CitationProfileGraphEdge[]
  caps: { max_nodes: number; max_edges: number }
  graph_truncated: boolean
  complete_list_url: string
  guidance: CitationProfileGuidance
}

export type CitationProfileCorrectionAction = 'confirm_relevant' | 'reject_mapping' | 'retract_event'
export type CitationProfileCorrectionState = 'none' | 'confirmed' | 'rejected' | 'event_retracted'

export interface CitationProfileCorrectionHistoryItem {
  correction_id: CitationProfileUuid
  action: CitationProfileCorrectionAction
  reason_code?: string
  created_at: string
}

export interface CitationProfileEvidenceItem {
  event_id: CitationProfileUuid
  run_id: CitationProfileUuid
  relation_id: CitationProfileUuid
  overlay: CitationProfileOverlay
  claim_code: 'answer_source_linked_to_page_at_resolution_time'
  message_id: CitationProfileUuid
  origin_reference_index: number
  source_knowledge_id: CitationProfileUuid
  source_result_id: string
  source_chunk_index: number | null
  knowledge_record_version: CitationProfileToken
  authoritative_knowledge_base_id: CitationProfileUuid
  page_uuid: CitationProfileUuid
  page_version_at_resolution: CitationProfileToken
  occurred_at: string
  resolved_at: string
  run_mapping_revision: CitationProfileToken
  run_universe_watermark: CitationProfileToken
  stale_mapping: boolean
  correction_state: CitationProfileCorrectionState
  correction_history: CitationProfileCorrectionHistoryItem[]
}

export interface CitationProfileEvidenceResponse {
  snapshot: CitationProfileSnapshot | null
  page: {
    page_uuid: CitationProfileUuid
    page_version: CitationProfileToken
    title: string
    slug: string
  }
  items: CitationProfileEvidenceItem[]
  next_cursor: string | null
  guidance: CitationProfileGuidance
}

export interface CitationProfileCorrectionRequest {
  expected_read_version: CitationProfileReadVersion
  idempotency_key: CitationProfileUuid
  action: CitationProfileCorrectionAction
  event_id: CitationProfileUuid
  page_uuid: CitationProfileUuid
  reason_code?: string
}

export interface CitationProfileCorrectionResponse {
  correction_id: CitationProfileUuid
  action: CitationProfileCorrectionAction
  snapshot: CitationProfileSnapshot
  result: 'applied'
}

export interface CitationProfileExportCreateRequest {
  expected_read_version: CitationProfileReadVersion
  idempotency_key: CitationProfileUuid
  format: 'json'
}

export type CitationProfileExportStatus = 'preparing' | 'ready' | 'expired' | 'revoked' | 'failed'

export interface CitationProfileExportCreateResponse {
  operation_id: CitationProfileUuid
  status: CitationProfileExportStatus
  snapshot?: CitationProfileSnapshot
  expires_at?: string
  download_url: string | null
}

export interface CitationProfileExportStatusResponse {
  operation_id: CitationProfileUuid
  status: CitationProfileExportStatus
  expires_at?: string
  download_url: string | null
  schema_version?: 'citation_profile_export_v1'
}

export interface CitationProfileDeleteRequest {
  expected_read_version?: CitationProfileReadVersion
  idempotency_key?: CitationProfileUuid
}

export interface CitationProfileDeleteResponse {
  operation_id: CitationProfileUuid
  status: 'accepted'
  receipt_code: 'profile_hidden_purge_scheduled'
}

export interface CitationProfileBlindDeleteResponse {
  operation_id: CitationProfileUuid
  status: 'accepted'
  receipt_code: 'request_accepted'
}

export interface CitationProfileTransport {
  get<T>(url: string, config?: unknown): Promise<T>
  post<T>(url: string, body?: unknown, config?: unknown): Promise<T>
  put<T>(url: string, body?: unknown, config?: unknown): Promise<T>
  delete<T>(url: string, body?: unknown, config?: unknown): Promise<T>
}

const csrfConfig = { headers: { 'X-WeKnora-CSRF': '1' } }

function encodePath(value: string): string {
  return encodeURIComponent(value)
}

function queryString(params: Record<string, string | number | undefined | null>): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') query.set(key, String(value))
  }
  const text = query.toString()
  return text ? `?${text}` : ''
}

function apiUrl(path: string): string {
  if (/^https?:\/\//i.test(path)) return path
  const base = getApiBaseUrl()
  if (!base || base === '/') return path
  return `${base}${path.startsWith('/') ? path : `/${path}`}`
}

function browserAuthHeaders(config?: unknown): Record<string, string> {
  const configured = ((config as { headers?: Record<string, string> } | undefined)?.headers || {})
  const headers: Record<string, string> = {
    ...configured,
    'X-WeKnora-CSRF': configured['X-WeKnora-CSRF'] || '1',
  }
  if (typeof localStorage !== 'undefined') {
    const token = localStorage.getItem('weknora_token')
    const tenantId = localStorage.getItem('weknora_selected_tenant_id')
    if (token) headers.Authorization = `Bearer ${token}`
    if (tenantId) headers['X-Tenant-ID'] = tenantId
  }
  return headers
}

async function fetchWithCsrf<T>(url: string, init: RequestInit, parse: 'json' | 'blob' = 'json'): Promise<T> {
  const response = await fetch(apiUrl(url), {
    credentials: 'same-origin',
    ...init,
    headers: browserAuthHeaders({ headers: init.headers as Record<string, string> | undefined }),
  })
  const payload = parse === 'blob' ? await response.blob() : await response.json().catch(() => null)
  if (!response.ok) throw payload || { status: response.status }
  return payload as T
}

async function deleteWithCsrf<T>(url: string, body?: unknown, config?: unknown): Promise<T> {
  return fetchWithCsrf<T>(url, {
    method: 'DELETE',
    headers: {
      'Content-Type': 'application/json',
      ...((config as { headers?: Record<string, string> } | undefined)?.headers || {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

async function downloadWithCsrf(url: string): Promise<Blob> {
  return fetchWithCsrf<Blob>(url, { method: 'GET' }, 'blob')
}

async function requestGet<T>(url: string, config?: unknown): Promise<T> {
  const { get } = await import('../../utils/request')
  return get<T>(url, config)
}

async function requestPost<T>(url: string, body?: unknown, config?: unknown): Promise<T> {
  const { post } = await import('../../utils/request')
  return post<T>(url, body, config)
}

async function requestPut<T>(url: string, body?: unknown, config?: unknown): Promise<T> {
  const { put } = await import('../../utils/request')
  return put<T>(url, body, config)
}

const defaultTransport: CitationProfileTransport = {
  get: requestGet,
  post: requestPost,
  put: requestPut,
  delete: deleteWithCsrf,
}

export function isCitationProfileGuidance(value: unknown): value is CitationProfileGuidance {
  const data = value as CitationProfileGuidance
  return !!data
    && data.kind === 'none'
    && data.reason === 'evidence_insufficient'
    && Array.isArray(data.candidates)
    && data.candidates.length === 0
}

export function createCitationProfileClient(transport: CitationProfileTransport = defaultTransport) {
  return {
    getStatus(kbId: string) {
      return transport.get<CitationProfileStatusResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/status`,
      )
    },
    updateEnrollment(kbId: string, body: CitationProfileEnrollmentRequest) {
      return transport.put<CitationProfileEnrollmentResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/enrollment`,
        body,
        csrfConfig,
      )
    },
    listNodes(kbId: string, params: { cursor?: string | null; page_size?: number } = {}) {
      return transport.get<CitationProfileNodeListResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/nodes${queryString(params)}`,
      )
    },
    getGraph(kbId: string) {
      return transport.get<CitationProfileGraphResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/graph`,
      )
    },
    getEvidence(kbId: string, pageUuid: string, params: { cursor?: string | null; page_size?: number } = {}) {
      return transport.get<CitationProfileEvidenceResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/nodes/${encodePath(pageUuid)}/evidence${queryString(params)}`,
      )
    },
    createCorrection(kbId: string, body: CitationProfileCorrectionRequest) {
      return transport.post<CitationProfileCorrectionResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/corrections`,
        body,
        csrfConfig,
      )
    },
    createExport(kbId: string, body: CitationProfileExportCreateRequest) {
      return transport.post<CitationProfileExportCreateResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/exports`,
        body,
        csrfConfig,
      )
    },
    getExport(kbId: string, operationId: string) {
      return transport.get<CitationProfileExportStatusResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/exports/${encodePath(operationId)}`,
        csrfConfig,
      )
    },
    downloadExport(kbId: string, operationId: string) {
      return downloadWithCsrf(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile/exports/${encodePath(operationId)}/download`,
      )
    },
    deleteCurrentProfile(kbId: string, body?: CitationProfileDeleteRequest) {
      return transport.delete<CitationProfileDeleteResponse>(
        `/api/v1/knowledgebase/${encodePath(kbId)}/citation-profile`,
        body,
        csrfConfig,
      )
    },
    blindDeleteScope(kbId: string) {
      return transport.delete<CitationProfileBlindDeleteResponse>(
        `/api/v1/citation-profile/scopes/${encodePath(kbId)}`,
        undefined,
        csrfConfig,
      )
    },
  }
}

export const citationProfileClient = createCitationProfileClient()