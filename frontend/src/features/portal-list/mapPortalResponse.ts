/**
 * Portal knowledge base list mapping (second-development feature, decision
 * 019). `GET /knowledge-bases?scope=portal` answers a single flat array:
 * own-tenant KBs (default list shape) plus shared entries carrying inline
 * `shared/share_id/organization_id/org_name/permission/source_tenant_id/
 * shared_at` fields; system-admin responses additionally carry `tenant_id`
 * on every entry (cross-tenant view).
 *
 * The server is the single source of truth for merging and dedup — this
 * module only adapts the flat rows into the two card shapes the list page
 * already renders (own card / shared card), it never re-implements the
 * own+shared merge semantics.
 */

export interface PortalListItem {
  id: string
  shared?: boolean
  share_id?: string
  organization_id?: string
  org_name?: string
  permission?: string
  source_tenant_id?: number | string
  shared_at?: string
  tenant_id?: number | string
  [key: string]: unknown
}

export interface PortalCards {
  /** Own-card shaped entries ({...kb, isMine: true}), server order. */
  own: Array<Record<string, unknown> & { id: string; isMine: true }>
  /** Shared-card shaped flat entries, server order. */
  shared: Array<Record<string, unknown> & {
    id: string
    isMine: false
    permission: string
    shared_at: string
    share_id: string
    organization_id?: string
    org_name?: string
  }>
  /** Entries whose tenant_id differs from the caller's tenant (admin view). */
  crossTenantIds: Array<string>
}

function sameTenant(a: number | string | undefined, b: number | string | null | undefined): boolean {
  if (a === undefined || a === null || a === '') return true // absent → assume own tenant
  if (b === undefined || b === null || b === '') return false
  return String(a) === String(b)
}

export function mapPortalItems(
  items: PortalListItem[],
  currentTenantId?: number | string | null,
): PortalCards {
  const cards: PortalCards = { own: [], shared: [], crossTenantIds: [] }
  items.forEach((item) => {
    if (item.shared && item.share_id) {
      cards.shared.push({
        ...item,
        isMine: false as const,
        permission: item.permission || 'viewer',
        shared_at: item.shared_at || '',
        share_id: item.share_id,
        organization_id: item.organization_id,
        org_name: item.org_name,
      })
      return
    }
    if (!sameTenant(item.tenant_id as number | string | undefined, currentTenantId)) {
      cards.crossTenantIds.push(item.id)
    }
    cards.own.push({ ...item, isMine: true as const })
  })
  return cards
}

/** Whether a card row should show the cross-tenant badge in the portal view. */
export function isCrossTenantItem(
  item: { id: string; tenant_id?: number | string },
  currentTenantId?: number | string | null,
): boolean {
  return !sameTenant(item.tenant_id, currentTenantId)
}
