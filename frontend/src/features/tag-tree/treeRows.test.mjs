import assert from 'node:assert/strict'
import test from 'node:test'

// NOTE: .test.mjs (not .ts) so `tsx --test` auto-discovers this file — see
// grepResultsGroup.test.mjs for the same pattern. TS sources import fine
// because tsx registers the loader for the whole process.
import {
  buildTagTreeRows,
  collectLeafTagIds,
  collectLeafTags,
  expandSelectionToLeafIds,
  findTagTreeNode,
  flattenTagTree,
  nodeHasAnyLeaf,
  pruneSelectionToTopmost,
  selectionLabel,
  tagTreeStats,
  tagTreeNodeAncestors,
} from './treeRows.ts'

function node(id, name, extra = {}, children = []) {
  return {
    id,
    name,
    parent_id: null,
    path: `/${name}`,
    level: 0,
    tag_id: null,
    is_leaf: true,
    is_auto_extract: false,
    sort_order: 0,
    created_at: '2026-08-24T00:00:00Z',
    updated_at: '2026-08-24T00:00:00Z',
    children,
    ...extra,
  }
}

// Fixture mirroring the experiment KB shape:
//   行业研究 (internal)
//     新能源 (internal)
//       锂电 (leaf, tag-1)
//     固收 (leaf, tag-2)
//   空分组 (internal, no leaves)
const TREE = [
  node('n-industry', '行业研究', { is_leaf: false, level: 0 }, [
    node('n-newenergy', '新能源', { is_leaf: false, level: 1, parent_id: 'n-industry' }, [
      node('n-lithium', '锂电', { tag_id: 'tag-1', level: 2, parent_id: 'n-newenergy' }),
    ]),
    node('n-fixedincome', '固收', { tag_id: 'tag-2', level: 1, parent_id: 'n-industry' }),
  ]),
  node('n-empty', '空分组', { is_leaf: false, level: 0 }),
]

test('buildTagTreeRows honours the expanded set', () => {
  const collapsed = buildTagTreeRows(TREE, new Set())
  assert.equal(collapsed.length, 2) // only roots
  assert.equal(collapsed[0].node.id, 'n-industry')
  assert.equal(collapsed[0].depth, 0)
  assert.ok(collapsed[0].hasChildren)

  const expanded = buildTagTreeRows(TREE, new Set(['n-industry']))
  assert.equal(expanded.length, 4) // roots + industry's direct children
  assert.equal(expanded[1].node.id, 'n-newenergy')
  assert.equal(expanded[1].depth, 1)
  assert.equal(expanded[2].node.id, 'n-fixedincome')
  assert.equal(expanded[2].depth, 1)

  const deep = buildTagTreeRows(TREE, new Set(['n-industry', 'n-newenergy']))
  assert.equal(deep.length, 5)
  assert.equal(deep[2].node.id, 'n-lithium')
  assert.equal(deep[2].depth, 2)
  assert.equal(deep[3].node.id, 'n-fixedincome')
  assert.equal(deep[3].depth, 1)
})

test('flattenTagTree and findTagTreeNode walk the whole tree', () => {
  assert.equal(flattenTagTree(TREE).length, 5)
  assert.equal(findTagTreeNode(TREE, 'n-lithium')?.tag_id, 'tag-1')
  assert.equal(findTagTreeNode(TREE, 'missing'), undefined)
})

test('collectLeafTagIds: leaf resolves to itself, parent to all descendant leaves', () => {
  const lithium = findTagTreeNode(TREE, 'n-lithium')
  assert.deepEqual(collectLeafTagIds(lithium), ['tag-1'])

  const newEnergy = findTagTreeNode(TREE, 'n-newenergy')
  assert.deepEqual(collectLeafTagIds(newEnergy), ['tag-1'])

  const industry = findTagTreeNode(TREE, 'n-industry')
  assert.deepEqual(collectLeafTagIds(industry), ['tag-1', 'tag-2'])

  const empty = findTagTreeNode(TREE, 'n-empty')
  assert.deepEqual(collectLeafTagIds(empty), [])
})

test('nodeHasAnyLeaf guards the pre-filter red line', () => {
  assert.ok(nodeHasAnyLeaf(findTagTreeNode(TREE, 'n-industry')))
  assert.ok(!nodeHasAnyLeaf(findTagTreeNode(TREE, 'n-empty')))
})

test('expandSelectionToLeafIds unions and deduplicates, reporting empty selections', () => {
  const both = expandSelectionToLeafIds(TREE, ['n-industry', 'n-lithium'])
  assert.deepEqual(both.tagIds, ['tag-1', 'tag-2']) // no duplicate tag-1
  assert.deepEqual(both.emptySelections, [])

  const withEmpty = expandSelectionToLeafIds(TREE, ['n-empty', 'n-fixedincome'])
  assert.deepEqual(withEmpty.tagIds, ['tag-2'])
  assert.deepEqual(withEmpty.emptySelections, ['n-empty'])

  const unknown = expandSelectionToLeafIds(TREE, ['gone'])
  assert.deepEqual(unknown.tagIds, [])
  assert.deepEqual(unknown.emptySelections, ['gone'])
})

test('selectionLabel annotates internal nodes with their leaf count', () => {
  assert.equal(selectionLabel(findTagTreeNode(TREE, 'n-lithium')), '锂电')
  assert.equal(selectionLabel(findTagTreeNode(TREE, 'n-industry')), '行业研究 (2)')
})

test('tagTreeNodeAncestors returns the trail excluding the node itself', () => {
  assert.deepEqual(tagTreeNodeAncestors(TREE, 'n-lithium'), ['n-industry', 'n-newenergy'])
  assert.deepEqual(tagTreeNodeAncestors(TREE, 'n-industry'), [])
  assert.deepEqual(tagTreeNodeAncestors(TREE, 'gone'), [])
})

test('tagTreeStats counts nodes and leaves', () => {
  assert.deepEqual(tagTreeStats(TREE), { nodes: 5, leaves: 2 })
  assert.deepEqual(tagTreeStats([]), { nodes: 0, leaves: 0 })
})

test('collectLeafTags keeps names alongside ids', () => {
  const industry = findTagTreeNode(TREE, 'n-industry')
  assert.deepEqual(collectLeafTags(industry), [
    { id: 'tag-1', name: '锂电' },
    { id: 'tag-2', name: '固收' },
  ])
})

test('pruneSelectionToTopmost keeps only topmost selected nodes', () => {
  // Parent selected together with a descendant: descendant is implied.
  assert.deepEqual(pruneSelectionToTopmost(TREE, ['n-industry', 'n-lithium']), ['n-industry'])
  // Sibling branches stay independent.
  assert.deepEqual(pruneSelectionToTopmost(TREE, ['n-lithium', 'n-fixedincome']), [
    'n-lithium',
    'n-fixedincome',
  ])
  // Deep selection without its ancestors is kept as-is.
  assert.deepEqual(pruneSelectionToTopmost(TREE, ['n-lithium']), ['n-lithium'])
})
