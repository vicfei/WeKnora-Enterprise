import { get, post, put, del } from '@/utils/request'

/**
 * Hierarchical tag tree API (second-development feature).
 *
 * Mirrors the Go handler contract in internal/handler/tag_tree.go:
 * all endpoints live under /api/v1/knowledge-bases/:id/tag-tree and answer
 * `{success: true, data: ...}`; the axios interceptor already unwrapped the
 * HTTP envelope, so these helpers return the business body as-is and typed
 * callers read `.data`.
 *
 * Tree semantics (标签树 2.0, v3.1.2): any node may carry tag_id (native
 * knowledge_tags record) — directories included; creating a child under a
 * tagged node no longer demotes it (选谁打谁, parent keeps its tag and all
 * tagging references); subtree filtering resolves to own + descendants union;
 * deletion physically cascades over every node that carries a tag.
 */
export interface TagTreeNode {
  id: string
  name: string
  parent_id: string | null
  path: string
  level: number
  /** Non-null ⟺ the node carries a native knowledge_tags row (directories may too). */
  tag_id: string | null
  is_leaf: boolean
  is_auto_extract: boolean
  sort_order: number
  created_at: string
  updated_at: string
  children?: TagTreeNode[]
}

/** Cascade counters returned by DELETE (all rows are removed physically). */
export interface TagTreeDeleteResult {
  deleted_nodes: number
  deleted_tags: number
  untagged_chunks: number
  removed_relations: number
  affected_tag_ids: string[]
}

/** Retrieval pre-filter resolution of one node (GET .../leaves). */
export interface TagTreeLeaves {
  node_id: string
  path: string
  is_leaf: boolean
  tag_ids: string[]
  count: number
}

export interface TagTreeEnvelope<T> {
  success: boolean
  data: T
}

/** Full nested tree; empty array means the KB has no tree yet. */
export function getTagTree(kbId: string): Promise<TagTreeEnvelope<TagTreeNode[]>> {
  return get(`/api/v1/knowledge-bases/${kbId}/tag-tree`) as unknown as Promise<TagTreeEnvelope<TagTreeNode[]>>
}

/**
 * Flat node list. `parent_id: 'root'` (or empty string) lists the top level;
 * omitting the param returns every node of the KB.
 */
export function listTagTreeNodes(
  kbId: string,
  params?: { parent_id?: string },
): Promise<TagTreeEnvelope<TagTreeNode[]>> {
  const query = params?.parent_id ? `?parent_id=${encodeURIComponent(params.parent_id)}` : ''
  return get(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes${query}`) as unknown as Promise<TagTreeEnvelope<TagTreeNode[]>>
}

export function getTagTreeNode(
  kbId: string,
  nodeId: string,
): Promise<TagTreeEnvelope<TagTreeNode>> {
  return get(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}`) as unknown as Promise<TagTreeEnvelope<TagTreeNode>>
}

/**
 * Create a node. New nodes start as leaves (the server auto-creates the
 * native tag); giving a leaf parent triggers its server-side demotion.
 */
export function createTagTreeNode(
  kbId: string,
  data: { name: string; parent_id?: string; sort_order?: number },
): Promise<TagTreeEnvelope<TagTreeNode>> {
  return post(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes`, data) as unknown as Promise<TagTreeEnvelope<TagTreeNode>>
}

/** Rename and/or re-sort; renaming recomputes the subtree paths and syncs the native tag name. */
export function updateTagTreeNode(
  kbId: string,
  nodeId: string,
  data: { name?: string; sort_order?: number },
): Promise<TagTreeEnvelope<TagTreeNode>> {
  return put(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}`, data) as unknown as Promise<TagTreeEnvelope<TagTreeNode>>
}

/** Physically delete the node and its whole subtree with cascade cleanup. */
export function deleteTagTreeNode(
  kbId: string,
  nodeId: string,
): Promise<TagTreeEnvelope<TagTreeDeleteResult>> {
  return del(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}`) as unknown as Promise<TagTreeEnvelope<TagTreeDeleteResult>>
}

/** Move a node to a new parent (empty parent_id = root level). */
export function attachTagTreeNode(kbId: string, nodeId: string): Promise<TagTreeEnvelope<TagTreeNode>> {
  return post(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}/attach-tag`, {})
}

export function moveTagTreeNode(
  kbId: string,
  nodeId: string,
  data: { parent_id: string },
): Promise<TagTreeEnvelope<TagTreeNode>> {
  return post(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}/move`, data) as unknown as Promise<TagTreeEnvelope<TagTreeNode>>
}

/**
 * Leaf tag ids under a node, ready for hybrid-search `tag_ids`.
 * An empty tag_ids array means the subtree has no leaf tags: callers must
 * treat that as "no results", never as "no filter".
 */
export function getTagTreeNodeLeaves(
  kbId: string,
  nodeId: string,
): Promise<TagTreeEnvelope<TagTreeLeaves>> {
  return get(`/api/v1/knowledge-bases/${kbId}/tag-tree/nodes/${nodeId}/leaves`) as unknown as Promise<TagTreeEnvelope<TagTreeLeaves>>
}
