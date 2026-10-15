import type { TagTreeNode } from './api'

/**
 * Pure helpers behind the hierarchical tag tree UI, following the
 * folderTree.ts pattern (component renders flattened rows; logic stays here
 * so it can be unit-tested without Vue).
 *
 * The whole tree is fetched once via GET /knowledge-bases/:id/tag-tree, so
 * every question the pickers ask ("what are this node's leaves?", "expand
 * this selection") is answered locally — the /leaves endpoint is reserved
 * for external callers (Java portal) that only know a node id.
 */

/** One rendered row of the flattened tree. */
export type TagTreeRow = {
  node: TagTreeNode
  depth: number
  hasChildren: boolean
}

/** Depth-first flatten honouring the expanded set at every level. */
export function buildTagTreeRows(
  roots: TagTreeNode[],
  expanded: Set<string>,
): TagTreeRow[] {
  const rows: TagTreeRow[] = []
  const walk = (nodes: TagTreeNode[], depth: number) => {
    nodes.forEach((node) => {
      const hasChildren = !!node.children?.length
      rows.push({ node, depth, hasChildren })
      if (hasChildren && expanded.has(node.id)) {
        walk(node.children!, depth + 1)
      }
    })
  }
  walk(roots, 0)
  return rows
}

/** Full depth-first flatten ignoring expansion state. */
export function flattenTagTree(roots: TagTreeNode[]): TagTreeNode[] {
  const nodes: TagTreeNode[] = []
  const walk = (list: TagTreeNode[]) => {
    list.forEach((node) => {
      nodes.push(node)
      if (node.children?.length) walk(node.children)
    })
  }
  walk(roots)
  return nodes
}

/** Find a node by id anywhere in the tree. */
export function findTagTreeNode(roots: TagTreeNode[], id: string): TagTreeNode | undefined {
  for (const node of roots) {
    if (node.id === id) return node
    const found = node.children?.length ? findTagTreeNode(node.children, id) : undefined
    if (found) return found
  }
  return undefined
}

function isLeafNode(node: TagTreeNode): boolean {
  // 标签树 2.0(v3.1.2):tag_id 非空 = 携带标签(可用于打标),与是否有
  // 子节点无关;is_leaf 服务端同义。目录也可以是标签(选谁打谁)。
  return node.tag_id !== null && node.tag_id !== undefined && node.tag_id !== ''
}

/**
 * Leaf tag ids of a node's own subtree, in tree order, deduplicated.
 * A leaf resolves to its own single tag id; an internal node collects every
 * descendant leaf. Empty array ⟺ the subtree cannot filter anything.
 */
export function collectLeafTagIds(node: TagTreeNode): string[] {
  // 标签树 2.0(v3.1.2):收集"自身标签 + 子树全部标签"的并集(B 语义)。
  // 旧实现在带标签节点上 return 停止递归——目录可携带标签后会漏掉子树。
  const ids: string[] = []
  const walk = (n: TagTreeNode) => {
    if (n.tag_id && !ids.includes(n.tag_id)) ids.push(n.tag_id)
    n.children?.forEach(walk)
  }
  walk(node)
  return ids
}

/** Whether a subtree contains at least one leaf tag (pre-filter red line). */
export function nodeHasAnyLeaf(node: TagTreeNode): boolean {
  return collectLeafTagIds(node).length > 0
}

/**
 * Expand a selection of node ids (leaf or internal) into the deduplicated
 * union of their leaf tag ids. Selecting an internal node means "filter by
 * everything below it"; selections that resolve to nothing are dropped from
 * the result but reported so the UI can warn about them.
 */
export function expandSelectionToLeafIds(
  roots: TagTreeNode[],
  selectedNodeIds: string[],
): { tagIds: string[]; emptySelections: string[] } {
  const tagIds: string[] = []
  const emptySelections: string[] = []
  selectedNodeIds.forEach((id) => {
    const node = findTagTreeNode(roots, id)
    if (!node) {
      emptySelections.push(id)
      return
    }
    const leaves = collectLeafTagIds(node)
    if (leaves.length === 0) {
      emptySelections.push(id)
      return
    }
    leaves.forEach((tagId) => {
      if (!tagIds.includes(tagId)) tagIds.push(tagId)
    })
  })
  return { tagIds, emptySelections }
}

/**
 * Display label for a selected node: the node name for a single node, the
 * "parent (n)" form when one selection actually stands for several leaves.
 */
export function selectionLabel(node: TagTreeNode): string {
  if (isLeafNode(node)) return node.name
  const count = collectLeafTagIds(node).length
  return count > 0 ? `${node.name} (${count})` : node.name
}

/** Ancestor node ids of a node (excluding itself), for auto-expanding rows. */
export function tagTreeNodeAncestors(roots: TagTreeNode[], id: string): string[] {
  const walk = (nodes: TagTreeNode[], trail: string[]): string[] | null => {
    for (const node of nodes) {
      if (node.id === id) return trail
      if (node.children?.length) {
        const found = walk(node.children, [...trail, node.id])
        if (found) return found
      }
    }
    return null
  }
  return walk(roots, []) ?? []
}

/**
 * Leaf tag {id, name} pairs of a node's subtree (same walk as
 * collectLeafTagIds, keeping names for mention expansion).
 */
export function collectLeafTags(node: TagTreeNode): Array<{ id: string; name: string }> {
  const tags: Array<{ id: string; name: string }> = []
  const seen = new Set<string>()
  const walk = (n: TagTreeNode) => {
    if (isLeafNode(n) && n.tag_id && !seen.has(n.tag_id)) {
      seen.add(n.tag_id)
      tags.push({ id: n.tag_id, name: n.name })
      return
    }
    n.children?.forEach(walk)
  }
  walk(node)
  return tags
}

/**
 * Drop selection entries that are already implied by an ancestor selection
 * (parent = whole group semantics): only the topmost selected nodes remain.
 */
export function pruneSelectionToTopmost(roots: TagTreeNode[], selectedIds: string[]): string[] {
  const selected = new Set(selectedIds)
  const kept: string[] = []
  const walk = (nodes: TagTreeNode[], implied: boolean) => {
    nodes.forEach((node) => {
      const isSelected = selected.has(node.id)
      if (isSelected && !implied) kept.push(node.id)
      walk(node.children || [], implied || isSelected)
    })
  }
  walk(roots, false)
  return kept
}

/** Simple totals for panel headers. */
export function tagTreeStats(roots: TagTreeNode[]): { nodes: number; leaves: number } {
  const all = flattenTagTree(roots)
  return {
    nodes: all.length,
    leaves: all.filter(isLeafNode).length,
  }
}
