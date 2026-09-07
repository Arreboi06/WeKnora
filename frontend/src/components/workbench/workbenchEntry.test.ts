import assert from 'node:assert/strict'
import test from 'node:test'

import {
  buildWorkbenchCommandPayload,
  buildWorkbenchJobPayload,
  buildWorkbenchStartPayload,
  shouldShowWorkbenchEntry,
  sanitizeWorkbenchAuditRows,
} from './workbenchEntry'

test('T2-L08 workbench entry is hidden unless a real session and explicit capability are present', () => {
  const eligibleBackends = ['local-protected-docker']
  assert.equal(shouldShowWorkbenchEntry({ sessionId: 'chat-1', capabilitySupported: true, eligibleBackends }), true)
  assert.equal(shouldShowWorkbenchEntry({ sessionId: ' ', capabilitySupported: true, eligibleBackends }), false)
  assert.equal(shouldShowWorkbenchEntry({ sessionId: 'chat-1', capabilitySupported: false, eligibleBackends }), false)
  assert.equal(shouldShowWorkbenchEntry({ sessionId: 'chat-1', capabilitySupported: true, eligibleBackends, embeddedMode: true }), false)
})

test('T2-L09 workbench entry fails closed without an eligible protected backend', () => {
  const options = (eligibleBackends: string[]) => ({
    sessionId: 'chat-1',
    capabilitySupported: true,
    eligibleBackends,
  } as unknown as Parameters<typeof shouldShowWorkbenchEntry>[0])
  assert.equal(shouldShowWorkbenchEntry(options([])), false)
  assert.equal(shouldShowWorkbenchEntry(options(['remote-unverified'])), false)
  assert.equal(shouldShowWorkbenchEntry(options(['local-protected-docker'])), true)
})

test('T2-L08 workbench payloads carry only local implementation snapshots', () => {
  const start = buildWorkbenchStartPayload({
    incarnationId: 'inc-1',
    sandboxConfigId: 'local-protected-docker',
    eligibleBackends: ['local-protected-docker'],
  })
  assert.equal(start.backend_type, 'docker')
  assert.equal(start.capability_snapshot['sandbox.workbench'], true)
  assert.deepEqual(start.policy_snapshot.eligible_backends, ['local-protected-docker'])
  assert.equal(start.policy_snapshot.scope, 'T2-LOCAL-IMPLEMENTATION')

  const job = buildWorkbenchJobPayload({
    workbenchId: 'wb-1',
    leaseEpoch: 0,
    startNonce: 'nonce-1',
  })
  assert.equal(job.backend_type, 'docker')
  assert.equal(job.resource_policy_snapshot.default_closed, true)

  const command = buildWorkbenchCommandPayload({
    command: 'echo ok',
    sequence: 2,
    leaseEpoch: 0,
    stateVersion: 1,
  })
  assert.equal(command.command, 'echo ok')
  assert.equal(command.expected_state_version, 1)
})

test('T2-L10 presentation candidate command carries explicit skill metadata', () => {
  const payload = buildWorkbenchCommandPayload({
    command: 'generate presentation',
    sequence: 1,
    leaseEpoch: 0,
    stateVersion: 0,
    skillName: 'presentations',
    skillOperation: 'create_presentation',
  } as unknown as Parameters<typeof buildWorkbenchCommandPayload>[0])
  const record = payload as unknown as Record<string, unknown>
  assert.equal(record.skill_name, 'presentations')
  assert.equal(record.skill_operation, 'create_presentation')
})

test('T2-L10 generated incarnation uses the persisted UUID length contract', () => {
  const payload = buildWorkbenchStartPayload({
    sandboxConfigId: 'local-protected-docker',
    eligibleBackends: ['local-protected-docker'],
  })
  assert.equal(payload.incarnation_id.length, 36)
  assert.match(payload.incarnation_id, /^[0-9a-f-]{36}$/)
})
test('T2-L08 command drafts fail closed before sending malformed commands', () => {
  assert.throws(() => buildWorkbenchCommandPayload({
    command: '   ',
    sequence: 1,
    leaseEpoch: 0,
    stateVersion: 0,
  }), /command/)
  assert.throws(() => buildWorkbenchCommandPayload({
    command: 'date',
    sequence: 0,
    leaseEpoch: 0,
    stateVersion: 0,
  }), /sequence/)
})

test('T2-L08 audit rows are sanitized again on the client boundary', () => {
  const rows = sanitizeWorkbenchAuditRows([
    {
      id: 'audit-1',
      action: 'workbench.job.started',
      payload: {
        backend_identity: 'container-secret',
        container_id: 'container-secret',
        state: 'RUNNING',
      },
    },
  ])
  assert.deepEqual(rows[0]?.payload, { state: 'RUNNING' })
})
