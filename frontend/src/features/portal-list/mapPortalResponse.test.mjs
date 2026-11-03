import assert from 'node:assert/strict'
import test from 'node:test'

import { buildPortalGroupStream, isCrossTenantItem, isPortalGroupHeader, mapPortalItems } from './mapPortalResponse.ts'

function ownKb(id, extra = {}) {
  return { id, name: `kb-${id}`, tenant_id: 10, ...extra }
}

function sharedKb(id, extra = {}) {
  return {
    id,
    name: `shared-${id}`,
    shared: true,
    share_id: `share-${id}`,
    organization_id: 'org-1',
    org_name: '研发部',
    permission: 'viewer',
    source_tenant_id: 20,
    shared_at: '2026-08-21T00:00:00Z',
    ...extra,
  }
}

test('mapPortalItems splits the flat portal response into own and shared cards', () => {
  const cards = mapPortalItems([ownKb('a'), sharedKb('b'), ownKb('c')], 10)
  assert.equal(cards.own.length, 2)
  assert.equal(cards.shared.length, 1)
  assert.equal(cards.own[0].isMine, true)
  assert.equal(cards.shared[0].isMine, false)
  assert.equal(cards.shared[0].share_id, 'share-b')
  assert.equal(cards.shared[0].org_name, '研发部')
  // Server order preserved within each bucket.
  assert.equal(cards.own.map(k => k.id).join(','), 'a,c')
})

test('mapPortalItems keeps shared rows shared even when tenant_id matches', () => {
  const cards = mapPortalItems([sharedKb('b', { tenant_id: 10 })], 10)
  assert.equal(cards.shared.length, 1)
  assert.equal(cards.own.length, 0)
})

test('mapPortalItems flags cross-tenant own rows for the admin view', () => {
  const cards = mapPortalItems([ownKb('a'), ownKb('x', { tenant_id: 99 })], 10)
  assert.deepEqual(cards.crossTenantIds, ['x'])
  assert.equal(isCrossTenantItem({ id: 'x', tenant_id: 99 }, 10), true)
  assert.equal(isCrossTenantItem({ id: 'a', tenant_id: 10 }, 10), false)
})

test('absent tenant_id is treated as the caller tenant (non-admin entries)', () => {
  const cards = mapPortalItems([ownKb('a', { tenant_id: undefined })], 10)
  assert.deepEqual(cards.crossTenantIds, [])
  assert.equal(isCrossTenantItem({ id: 'a' }, 10), false)
})

test('tenant comparison is string-safe across number/string encodings', () => {
  assert.equal(isCrossTenantItem({ id: 'a', tenant_id: '10' }, 10), false)
  assert.equal(isCrossTenantItem({ id: 'a', tenant_id: 10 }, '10'), false)
  assert.equal(isCrossTenantItem({ id: 'a', tenant_id: '20' }, 10), true)
})

// ── v2 grouped stream ──────────────────────────────────────────────────
test('buildPortalGroupStream groups personal/team/all/shared in order', () => {
  const stream = buildPortalGroupStream([
    { id: 'p1', tenant_id: 10, tenant_name: '我的空间', space_type: 'personal', my_role: 'owner' },
    { id: 't1', tenant_id: 20, tenant_name: '投研团队', space_type: 'team', my_role: 'contributor' },
    { id: 'a1', shared: true, share_id: 's1', permission: 'viewer', shared_at: 'x', space_type: 'all', tenant_name: '公司公共库' },
    { id: 'x1', shared: true, share_id: 's2', permission: 'editor', shared_at: 'x', space_type: 'team', tenant_name: '兄弟团队' },
  ], 10)
  const headers = stream.filter(isPortalGroupHeader)
  assert.equal(headers.length, 4)
  assert.deepEqual(headers.map(h => h.__portalGroup), ['personal', 'team', 'all', 'shared'])
  assert.equal(headers[0].role, 'owner')
  assert.equal(headers[2].count, 1)
  const ids = stream.filter(i => !isPortalGroupHeader(i)).map(i => i.id)
  assert.deepEqual(ids, ['p1', 't1', 'a1', 'x1'])
})

test('empty groups are skipped', () => {
  const stream = buildPortalGroupStream([{ id: 't1', tenant_id: 20, space_type: 'team' }], 20)
  const headers = stream.filter(isPortalGroupHeader)
  assert.equal(headers.length, 1)
  assert.equal(headers[0].__portalGroup, 'team')
})
