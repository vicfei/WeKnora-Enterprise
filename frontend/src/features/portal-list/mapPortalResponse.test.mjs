import assert from 'node:assert/strict'
import test from 'node:test'

import { isCrossTenantItem, mapPortalItems } from './mapPortalResponse.ts'

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
