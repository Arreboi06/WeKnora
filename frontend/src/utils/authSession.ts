import type { MembershipInfo, TenantInfo } from '../api/auth/index'
import { userInfoFromApi, type UserInfo } from './userInfo'

export interface LoginSessionStore {
  setUser(user: UserInfo): void
  setToken(token: string): void
  setRefreshToken(token: string): void
  setTenant(tenant: TenantInfo | null): void
  setMemberships(memberships: MembershipInfo[]): void
  setSelectedTenant(tenantId: number | null, tenantName: string | null): void
}

/**
 * Persist one successful login/auto-setup response through the sole frontend
 * session contract. Newer RBAC endpoints call the selected workspace
 * `active_tenant`; older servers call it `tenant`. The user's tenant_id remains
 * the immutable home workspace and must never be replaced by the active one.
 */
export function persistLoginSession(store: LoginSessionStore, response: any): boolean {
  if (!response?.user || !response?.token) return false

  const activeTenant = response.active_tenant || response.tenant || null
  const homeTenantIdRaw = response.user.tenant_id ?? activeTenant?.id ?? ''
  store.setUser(userInfoFromApi(response.user, homeTenantIdRaw))
  store.setToken(response.token)
  if (response.refresh_token) store.setRefreshToken(response.refresh_token)

  if (activeTenant) {
    store.setTenant({
      id: String(activeTenant.id) || '',
      name: activeTenant.name || '',
      owner_id: activeTenant.owner_id || response.user.id || '',
      description: activeTenant.description,
      status: activeTenant.status,
      business: activeTenant.business,
      storage_quota: activeTenant.storage_quota,
      storage_used: activeTenant.storage_used,
      created_at: activeTenant.created_at || new Date().toISOString(),
      updated_at: activeTenant.updated_at || new Date().toISOString(),
    })
  } else {
    store.setTenant(null)
  }

  if (Array.isArray(response.memberships)) {
    store.setMemberships(response.memberships)
  }

  const activeID = Number(activeTenant?.id)
  const homeID = Number(homeTenantIdRaw)
  if (Number.isFinite(activeID) && activeID > 0 && Number.isFinite(homeID) && homeID > 0 && activeID !== homeID) {
    store.setSelectedTenant(activeID, activeTenant?.name || null)
  } else {
    store.setSelectedTenant(null, null)
  }
  return true
}
