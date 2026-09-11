export interface UserInfo {
  id: string
  username: string
  email: string
  avatar?: string
  tenant_id: string
  can_access_all_tenants?: boolean
  preferences?: {
    last_active_tenant_id?: number | null
    oidc_only_login?: boolean
  }
  is_system_admin?: boolean
  created_at: string
  updated_at: string
}

/** Normalize every backend user shape through one strict permission boundary. */
export function userInfoFromApi(
  user: any,
  fallbackTenantId?: string | number | null,
): UserInfo {
  const rawTenantId =
    user?.tenant_id !== undefined && user?.tenant_id !== null && user.tenant_id !== ''
      ? user.tenant_id
      : fallbackTenantId ?? ''
  const tenantID = Number(rawTenantId) > 0 ? rawTenantId : ''
  return {
    id: user?.id || '',
    username: user?.username || '',
    email: user?.email || '',
    avatar: user?.avatar,
    tenant_id: String(tenantID) || '',
    can_access_all_tenants: user?.can_access_all_tenants === true,
    is_system_admin: user?.is_system_admin === true,
    preferences: user?.preferences,
    created_at: user?.created_at || new Date().toISOString(),
    updated_at: user?.updated_at || new Date().toISOString(),
  }
}
