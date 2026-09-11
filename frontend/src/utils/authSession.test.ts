import assert from 'node:assert/strict'
import test from 'node:test'

import { persistLoginSession } from './authSession'

function sessionStore() {
  const calls: Record<string, unknown> = {}
  return {
    calls,
    setUser: (value: unknown) => { calls.user = value },
    setToken: (value: unknown) => { calls.token = value },
    setRefreshToken: (value: unknown) => { calls.refreshToken = value },
    setTenant: (value: unknown) => { calls.tenant = value },
    setMemberships: (value: unknown) => { calls.memberships = value },
    setSelectedTenant: (id: unknown, name: unknown) => { calls.selectedTenant = [id, name] },
  }
}

test('persists the active_tenant auto-setup contract exactly once', () => {
  const store = sessionStore()
  const persisted = persistLoginSession(store, {
    success: true,
    token: 'access-token',
    refresh_token: 'refresh-token',
    user: { id: 'user-1', username: 'admin', email: 'admin@example.test', tenant_id: 7 },
    active_tenant: { id: 9, name: 'Active', created_at: '2026-09-11T00:00:00Z', updated_at: '2026-09-11T00:00:00Z' },
    memberships: [{ tenant_id: 7, role: 'owner' }, { tenant_id: 9, role: 'viewer' }],
  })

  assert.equal(persisted, true)
  assert.equal((store.calls.user as any).tenant_id, '7')
  assert.equal((store.calls.tenant as any).id, '9')
  assert.equal(store.calls.token, 'access-token')
  assert.equal(store.calls.refreshToken, 'refresh-token')
  assert.deepEqual(store.calls.memberships, [
    { tenant_id: 7, role: 'owner' },
    { tenant_id: 9, role: 'viewer' },
  ])
  assert.deepEqual(store.calls.selectedTenant, [9, 'Active'])
})

test('persists a valid tenantless session without inventing a tenant', () => {
  const store = sessionStore()
  const persisted = persistLoginSession(store, {
    success: true,
    token: 'tenantless-token',
    user: { id: 'user-2', username: 'pending', email: 'pending@example.test', tenant_id: null },
    active_tenant: null,
    memberships: [],
  })

  assert.equal(persisted, true)
  assert.equal((store.calls.user as any).tenant_id, '')
  assert.equal(store.calls.tenant, null)
  assert.deepEqual(store.calls.selectedTenant, [null, null])
})

test('rejects a success-shaped response without identity or token', () => {
  const store = sessionStore()
  assert.equal(persistLoginSession(store, { success: true, active_tenant: { id: 1 } }), false)
  assert.deepEqual(store.calls, {})
})
