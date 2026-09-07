export const WORKBENCH_LOCAL_SCOPE = 'T2-LOCAL-IMPLEMENTATION'
export const WORKBENCH_LOCAL_DOCKER_BACKEND = 'local-protected-docker'
export const WORKBENCH_CAPABILITY_KEY = 'sandbox.workbench'
export const WORKBENCH_PRESENTATION_SKILL_NAME = 'presentations'
export const WORKBENCH_PRESENTATION_SKILL_OPERATION = 'create_presentation'

export interface WorkbenchEntryOptions {
  sessionId?: string | null
  capabilitySupported?: boolean
  eligibleBackends?: string[] | null
  embeddedMode?: boolean
}

export interface WorkbenchStartPayload {
  incarnation_id: string
  sandbox_config_id: string
  backend_type: 'docker'
  capability_snapshot: Record<string, unknown>
  policy_snapshot: Record<string, unknown>
}

export interface WorkbenchJobPayload {
  workbench_id: string
  expected_lease_epoch: number
  start_nonce: string
  backend_type: 'docker'
  resource_policy_snapshot: Record<string, unknown>
}

export interface WorkbenchCommandPayload {
  sequence: number
  expected_lease_epoch: number
  expected_state_version: number
  command: string
  work_dir?: string
  stdin?: string
  timeout_ms?: number
  env?: Record<string, string>
  skill_name?: string
  skill_operation?: string
}

export interface WorkbenchAuditLike {
  id?: string
  action?: string
  payload?: Record<string, unknown> | null
}

const REDACTED_PAYLOAD_KEYS = new Set([
  'backend_identity',
  'backendidentity',
  'container_id',
  'containerid',
])

export function shouldShowWorkbenchEntry(options: WorkbenchEntryOptions): boolean {
  return Boolean(
    options.capabilitySupported === true &&
    !options.embeddedMode &&
    options.sessionId &&
    options.sessionId.trim() &&
    normalizeEligibleBackends(options.eligibleBackends).length > 0,
  )
}

export function buildWorkbenchStartPayload(options: {
  incarnationId?: string
  sandboxConfigId?: string
  eligibleBackends?: string[]
} = {}): WorkbenchStartPayload {
  const eligibleBackends = normalizeEligibleBackends(options.eligibleBackends)
  const sandboxConfigId = nonEmpty(options.sandboxConfigId) || eligibleBackends[0]
  if (!sandboxConfigId || !eligibleBackends.includes(sandboxConfigId)) {
    throw new Error('eligible workbench backend is required')
  }
  return {
    incarnation_id: nonEmpty(options.incarnationId) || randomWorkbenchIncarnationID(),
    sandbox_config_id: sandboxConfigId,
    backend_type: 'docker',
    capability_snapshot: {
      [WORKBENCH_CAPABILITY_KEY]: true,
      eligible_backends: eligibleBackends,
    },
    policy_snapshot: {
      scope: WORKBENCH_LOCAL_SCOPE,
      default_closed: true,
      backend_type: 'docker',
      eligible_backends: eligibleBackends,
    },
  }
}

export function buildWorkbenchJobPayload(options: {
  workbenchId: string
  leaseEpoch: number
  startNonce?: string
  backend?: string
}): WorkbenchJobPayload {
  const workbenchId = nonEmpty(options.workbenchId)
  if (!workbenchId) throw new Error('workbench id is required')
  if (!Number.isInteger(options.leaseEpoch) || options.leaseEpoch < 0) {
    throw new Error('lease epoch is required')
  }
  return {
    workbench_id: workbenchId,
    expected_lease_epoch: options.leaseEpoch,
    start_nonce: nonEmpty(options.startNonce) || randomWorkbenchToken('nonce'),
    backend_type: 'docker',
    resource_policy_snapshot: {
      scope: WORKBENCH_LOCAL_SCOPE,
      default_closed: true,
      backend: nonEmpty(options.backend) || WORKBENCH_LOCAL_DOCKER_BACKEND,
    },
  }
}

export function buildWorkbenchCommandPayload(options: {
  command: string
  sequence: number
  leaseEpoch: number
  stateVersion: number
  workDir?: string
  stdin?: string
  timeoutMs?: number
  env?: Record<string, string>
  skillName?: string
  skillOperation?: string
}): WorkbenchCommandPayload {
  const command = nonEmpty(options.command)
  if (!command) throw new Error('command is required')
  if (!Number.isInteger(options.sequence) || options.sequence <= 0) {
    throw new Error('sequence must be positive')
  }
  if (!Number.isInteger(options.leaseEpoch) || options.leaseEpoch < 0) {
    throw new Error('lease epoch is required')
  }
  if (!Number.isInteger(options.stateVersion) || options.stateVersion < 0) {
    throw new Error('state version is required')
  }

  const payload: WorkbenchCommandPayload = {
    sequence: options.sequence,
    expected_lease_epoch: options.leaseEpoch,
    expected_state_version: options.stateVersion,
    command,
  }
  if (nonEmpty(options.workDir)) payload.work_dir = nonEmpty(options.workDir)
  if (options.stdin) payload.stdin = options.stdin
  if (options.timeoutMs && options.timeoutMs > 0) payload.timeout_ms = options.timeoutMs
  const env = normalizeEnv(options.env)
  if (Object.keys(env).length > 0) payload.env = env
  const skillName = nonEmpty(options.skillName).toLowerCase()
  const skillOperation = nonEmpty(options.skillOperation)
  if (skillName || skillOperation) {
    if (skillName !== WORKBENCH_PRESENTATION_SKILL_NAME || skillOperation !== WORKBENCH_PRESENTATION_SKILL_OPERATION) {
      throw new Error('unsupported Workbench skill metadata')
    }
    payload.skill_name = skillName
    payload.skill_operation = skillOperation
  }
  return payload
}

export function sanitizeWorkbenchAuditRows<T extends WorkbenchAuditLike>(rows: T[] | null | undefined): Array<T & { payload: Record<string, unknown> }> {
  if (!Array.isArray(rows)) return []
  return rows.map((row) => {
    const payload = row.payload && typeof row.payload === 'object'
      ? sanitizePayload(row.payload)
      : {}
    return { ...row, payload }
  })
}

export function normalizeEligibleBackends(backends: string[] | null | undefined): string[] {
  if (!Array.isArray(backends)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of backends) {
    const value = nonEmpty(raw)
    if (value !== WORKBENCH_LOCAL_DOCKER_BACKEND || seen.has(value)) continue
    seen.add(value)
    out.push(value)
  }
  return out
}

export function randomWorkbenchIncarnationID(): string {
  return randomWorkbenchUUID()
}

export function randomWorkbenchToken(prefix: string): string {
  const safePrefix = nonEmpty(prefix)?.replace(/[^A-Za-z0-9_-]/g, '') || 'wb'
  return safePrefix + '-' + randomWorkbenchUUID()
}

function randomWorkbenchUUID(): string {
  const randomUUID = globalThis.crypto?.randomUUID?.()
  if (randomUUID) return randomUUID
  const bytes = new Uint8Array(16)
  if (!globalThis.crypto?.getRandomValues) throw new Error('secure random source unavailable')
  globalThis.crypto.getRandomValues(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return hex.slice(0, 8) + '-' + hex.slice(8, 12) + '-' + hex.slice(12, 16) + '-' + hex.slice(16, 20) + '-' + hex.slice(20)
}
function sanitizePayload(payload: Record<string, unknown>): Record<string, unknown> {
  const copy: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(payload)) {
    if (REDACTED_PAYLOAD_KEYS.has(key.trim().toLowerCase())) continue
    copy[key] = value
  }
  return copy
}

function normalizeEnv(env: Record<string, string> | null | undefined): Record<string, string> {
  if (!env) return {}
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(env)) {
    const normalizedKey = nonEmpty(key)
    if (!normalizedKey || typeof value !== 'string') continue
    out[normalizedKey] = value
  }
  return out
}

function nonEmpty(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
