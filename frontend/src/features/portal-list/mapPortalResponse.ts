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
  /** v2 aggregation metadata (backend listKnowledgeBasesPortal) */
  tenant_name?: string
  space_type?: 'personal' | 'team' | 'all' | string
  my_role?: string
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

// ── v2 grouped portal view ──────────────────────────────────────────────

export type PortalGroupKey = 'personal' | 'team' | 'all' | 'shared'

export interface PortalGroupHeader {
  __portalGroup: PortalGroupKey
  labelKey: string
  count: number
  role?: string
}

const GROUP_ORDER: PortalGroupKey[] = ['personal', 'team', 'all', 'shared']
const GROUP_LABEL_KEYS: Record<PortalGroupKey, string> = {
  personal: 'knowledgeList.portalGroupPersonal',
  team: 'knowledgeList.portalGroupTeam',
  all: 'knowledgeList.portalGroupAll',
  shared: 'knowledgeList.portalGroupShared',
}

function classify(item: PortalListItem, isShared: boolean): PortalGroupKey {
  if (isShared) {
    // shared entries from an `all` space belong to the company-public group
    return item.space_type === 'all' ? 'all' : 'shared'
  }
  if (item.space_type === 'personal') return 'personal'
  if (item.space_type === 'all') return 'all'
  return 'team'
}

/**
 * Interleaves group header markers into the flat portal card stream so the
 * existing single card loop can render "个人/团队/公司公共/共享给我"
 * sections without duplicating the card template. The first my_role seen in
 * a group rides on its header (own spaces have a stable role per user).
 */
export function buildPortalGroupStream(
  items: PortalListItem[],
  currentTenantId?: number | string | null,
): Array<PortalGroupHeader | (Record<string, unknown> & { id: string; isMine: boolean })> {
  const cards = mapPortalItems(items, currentTenantId)
  const buckets: Record<PortalGroupKey, Array<Record<string, unknown> & { id: string; isMine: boolean }>> = {
    personal: [], team: [], all: [], shared: [],
  }
  ;[...cards.own, ...cards.shared].forEach((card) => {
    buckets[classify(card as PortalListItem, card.isMine === false)].push(card as any)
  })
  const stream: Array<PortalGroupHeader | any> = []
  for (const key of GROUP_ORDER) {
    if (buckets[key].length === 0) continue
    const role = (buckets[key].find((c) => (c as any).my_role) as any)?.my_role
    stream.push({ __portalGroup: key, labelKey: GROUP_LABEL_KEYS[key], count: buckets[key].length, role })
    stream.push(...buckets[key])
  }
  return stream
}

export function isPortalGroupHeader(item: unknown): item is PortalGroupHeader {
  return typeof item === 'object' && item !== null && '__portalGroup' in item
}
