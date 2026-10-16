<template>
  <div class="kb-tag-tree-panel">
    <div class="tag-tree-header">
      <h2 class="tag-tree-title">{{ $t('knowledgeEditor.tagTree.title') }}</h2>
      <span v-if="stats.nodes > 0" class="tag-tree-stats">
        {{ $t('knowledgeEditor.tagTree.stats', { nodes: stats.nodes, leaves: stats.leaves }) }}
      </span>
      <div class="tag-tree-header-actions">
        <t-button variant="text" shape="square" size="small" :title="$t('knowledgeEditor.tagTree.refresh')"
          :disabled="loading" @click="loadTree">
          <template #icon><t-icon name="refresh" /></template>
        </t-button>
        <t-button theme="primary" size="small" @click="openCreate">
          <template #icon><t-icon name="add" /></template>
          {{ $t('knowledgeEditor.tagTree.create') }}
        </t-button>
      </div>
    </div>
    <p class="tag-tree-desc">{{ $t('knowledgeEditor.tagTree.description') }}</p>

    <div class="tag-tree-body">
      <div v-if="loading && rows.length === 0" class="tag-tree-loading">
        <t-loading size="small" />
        <span>{{ $t('organization.share.loading') }}</span>
      </div>

      <div v-else-if="rows.length === 0" class="tag-tree-empty">
        <t-empty :description="$t('knowledgeEditor.tagTree.empty')" />
      </div>

      <div v-else class="tag-tree-rows" role="tree">
        <div v-for="row in rows" :key="row.node.id" class="tag-tree-row" role="treeitem"
          :style="{ '--tag-tree-depth': row.depth }" :class="{ 'is-leaf': row.node.is_leaf }">
          <button type="button" class="tag-tree-caret" :class="{ invisible: !row.hasChildren }"
            :aria-expanded="expanded.has(row.node.id)" :aria-label="$t('knowledgeEditor.tagTree.toggleExpand')"
            @click="toggleExpand(row.node.id)">
            <t-icon :name="expanded.has(row.node.id) ? 'chevron-down' : 'chevron-right'" size="14px" />
          </button>

          <template v-if="renamingId === row.node.id">
            <t-input v-model="renamingName" size="small" class="tag-tree-rename-input" autofocus
              :maxlength="255" :placeholder="$t('knowledgeEditor.tagTree.namePlaceholder')"
              @enter="commitRename(row.node)" @blur="commitRename(row.node)" />
          </template>
          <template v-else>
            <span class="tag-tree-name" :title="row.node.path">{{ row.node.name }}</span>
            <!-- 标签树 2.0:目录可同时携带标签——有子节点显示"分组",带 tag_id 再加"标签"徽章 -->
            <t-tag v-if="row.node.children?.length" size="small" variant="light" theme="default">
              {{ $t('knowledgeEditor.tagTree.groupBadge') }}
            </t-tag>
            <t-tag v-if="row.node.is_leaf" size="small" variant="light" theme="success">
              {{ $t('knowledgeEditor.tagTree.leafBadge') }}
            </t-tag>
            <span class="tag-tree-row-actions">
              <t-tooltip v-if="!row.node.tag_id" :content="$t('knowledgeEditor.tagTree.attachTag')" placement="top">
                <t-button variant="text" shape="square" size="small" :disabled="saving" @click="handleAttachTag(row.node)">
                  <template #icon><t-icon name="discount" size="14px" /></template>
                </t-button>
              </t-tooltip>
              <t-tooltip :content="$t('knowledgeEditor.tagTree.sortUp')" placement="top">
                <t-button variant="text" shape="square" size="small" :disabled="sorting" @click="handleSort(row.node, 'up')">
                  <template #icon><t-icon name="arrow-up" size="14px" /></template>
                </t-button>
              </t-tooltip>
              <t-tooltip :content="$t('knowledgeEditor.tagTree.sortDown')" placement="top">
                <t-button variant="text" shape="square" size="small" :disabled="sorting" @click="handleSort(row.node, 'down')">
                  <template #icon><t-icon name="arrow-down" size="14px" /></template>
                </t-button>
              </t-tooltip>
              <t-tooltip :content="$t('knowledgeEditor.tagTree.rename')" placement="top">
                <t-button variant="text" shape="square" size="small" @click="startRename(row.node)">
                  <template #icon><t-icon name="edit" size="14px" /></template>
                </t-button>
              </t-tooltip>
              <t-tooltip :content="$t('knowledgeEditor.tagTree.move')" placement="top">
                <t-button variant="text" shape="square" size="small" @click="openMove(row.node)">
                  <template #icon><t-icon name="move" size="14px" /></template>
                </t-button>
              </t-tooltip>
              <t-tooltip :content="$t('knowledgeEditor.tagTree.delete')" placement="top">
                <t-button variant="text" shape="square" size="small" theme="danger" @click="confirmDelete(row.node)">
                  <template #icon><t-icon name="delete" size="14px" /></template>
                </t-button>
              </t-tooltip>
            </span>
          </template>
        </div>
      </div>
    </div>

    <!-- 新建节点 -->
    <t-dialog v-model:visible="createVisible" :header="$t('knowledgeEditor.tagTree.create')"
      :confirm-btn="{ content: $t('common.confirm'), theme: 'primary', loading: saving }"
      :cancel-btn="{ content: $t('common.cancel') }" :close-on-overlay-click="false" @confirm="handleCreate">
      <div class="tag-tree-form">
        <div class="tag-tree-form-item">
          <label class="tag-tree-form-label">{{ $t('knowledgeEditor.tagTree.parentLabel') }}</label>
          <t-select v-model="createParentId" clearable :placeholder="$t('knowledgeEditor.tagTree.rootLevel')">
            <t-option v-for="opt in createParentOptions" :key="opt.id" :value="opt.id" :label="opt.path" />
          </t-select>
        </div>
        <div class="tag-tree-form-item">
          <label class="tag-tree-form-label">{{ $t('knowledgeEditor.tagTree.nameLabel') }}</label>
          <t-input v-model="createName" :maxlength="255" :placeholder="$t('knowledgeEditor.tagTree.namePlaceholder')" />
        </div>
      </div>
    </t-dialog>

    <!-- 移动节点 -->
    <t-dialog v-model:visible="moveVisible" :header="$t('knowledgeEditor.tagTree.moveTitle', { name: movingNode?.name || '' })"
      :confirm-btn="{ content: $t('common.confirm'), theme: 'primary', loading: saving, disabled: !moveTargetValid }"
      :cancel-btn="{ content: $t('common.cancel') }" :close-on-overlay-click="false" @confirm="handleMove">
      <div class="tag-tree-form">
        <div class="tag-tree-form-item">
          <label class="tag-tree-form-label">{{ $t('knowledgeEditor.tagTree.moveTarget') }}</label>
          <t-select v-model="moveTargetId" clearable :placeholder="$t('knowledgeEditor.tagTree.rootLevel')">
            <t-option v-for="opt in moveTargetOptions" :key="opt.id" :value="opt.id" :label="opt.path" />
          </t-select>
        </div>
      </div>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  attachTagTreeNode,
  createTagTreeNode,
  deleteTagTreeNode,
  getTagTree,
  moveTagTreeNode,
  updateTagTreeNode,
  type TagTreeNode,
} from './api'
import { buildTagTreeRows, flattenTagTree, findTagTreeNode, tagTreeStats } from './treeRows'

const { t } = useI18n()

interface Props {
  kbId: string
  /** Whether the hosting editor section is currently shown. */
  active?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  active: false,
})

const tree = ref<TagTreeNode[]>([])
const loading = ref(false)
const saving = ref(false)
const sorting = ref(false)
const expanded = ref(new Set<string>())

const createVisible = ref(false)
const createParentId = ref('')
const createName = ref('')
const renamingId = ref('')
const renamingName = ref('')
const moveVisible = ref(false)
const movingNode = ref<TagTreeNode | null>(null)
const moveTargetId = ref('')

const rows = computed(() => buildTagTreeRows(tree.value, expanded.value))
const stats = computed(() => tagTreeStats(tree.value))
const allNodes = computed(() => flattenTagTree(tree.value))

const createParentOptions = computed(() =>
  allNodes.value.map((n) => ({ id: n.id, path: n.path, is_leaf: n.is_leaf })),
)


const moveTargetOptions = computed(() => {
  const moving = movingNode.value
  if (!moving) return []
  // Front-end ring guard: the target may not be the node itself or anything
  // inside its subtree; the server re-checks (decision: backend is authority).
  return allNodes.value
    .filter((n) => n.id !== moving.id && !n.path.startsWith(`${moving.path}/`))
    .map((n) => ({ id: n.id, path: n.path, is_leaf: n.is_leaf }))
})

const moveTargetValid = computed(() => {
  const moving = movingNode.value
  if (!moving) return false
  if (!moveTargetId.value) return moving.parent_id !== null // root move only valid when not already root
  const target = findTagTreeNode(tree.value, moveTargetId.value)
  if (!target) return false
  if (target.id === moving.parent_id) return false // no-op
  return target.id !== moving.id && !target.path.startsWith(`${moving.path}/`)
})


function toggleExpand(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function loadTree() {
  if (!props.kbId) return
  loading.value = true
  try {
    const res = await getTagTree(props.kbId)
    tree.value = Array.isArray(res.data) ? res.data : []
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('knowledgeEditor.tagTree.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  createParentId.value = ''
  createName.value = ''
  createVisible.value = true
}

async function handleCreate() {
  const name = createName.value.trim()
  if (!name) {
    MessagePlugin.warning(t('knowledgeEditor.tagTree.nameRequired'))
    return
  }
  saving.value = true
  try {
    await createTagTreeNode(props.kbId, {
      name,
      parent_id: createParentId.value || '',
    })
    MessagePlugin.success(t('knowledgeEditor.tagTree.createSuccess'))
    createVisible.value = false
    if (createParentId.value) {
      const next = new Set(expanded.value)
      next.add(createParentId.value)
      expanded.value = next
    }
    await loadTree()
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('knowledgeEditor.tagTree.operationFailed'))
  } finally {
    saving.value = false
  }
}

function startRename(node: TagTreeNode) {
  renamingId.value = node.id
  renamingName.value = node.name
}

async function commitRename(node: TagTreeNode) {
  if (renamingId.value !== node.id) return
  const name = renamingName.value.trim()
  renamingId.value = ''
  if (!name || name === node.name) return
  try {
    await updateTagTreeNode(props.kbId, node.id, { name })
    MessagePlugin.success(t('knowledgeEditor.tagTree.renameSuccess'))
    await loadTree()
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('knowledgeEditor.tagTree.operationFailed'))
  }
}

/** Sibling list containing `id` (root list when the node is top-level). */
function siblingsOf(id: string): TagTreeNode[] {
  let found: TagTreeNode[] | null = null
  const walk = (list: TagTreeNode[]) => {
    for (const n of list) {
      if (n.id === id) {
        found = list
        return
      }
      if (n.children?.length) walk(n.children)
    }
  }
  walk(tree.value)
  return found ?? []
}

async function handleAttachTag(node: TagTreeNode) {
  saving.value = true
  try {
    await attachTagTreeNode(props.kbId, node.id)
    MessagePlugin.success(t('knowledgeEditor.tagTree.attachSuccess'))
    await loadTree()
  } catch (e: any) {
    MessagePlugin.error(e?.response?.data?.error?.message || t('knowledgeEditor.tagTree.operationFailed'))
  } finally {
    saving.value = false
  }
}

async function handleSort(node: TagTreeNode, dir: 'up' | 'down') {
  const siblings = siblingsOf(node.id)
  const idx = siblings.findIndex((s) => s.id === node.id)
  const j = dir === 'up' ? idx - 1 : idx + 1
  if (idx < 0 || j < 0 || j >= siblings.length) return
  const other = siblings[j]
  const a = node.sort_order
  const b = other.sort_order
  // Equal sort values tie-break by path on the server, so force a strict
  // order instead of a no-op swap.
  const updates: Array<[string, number]> =
    a === b
      ? dir === 'up'
        ? [[node.id, b], [other.id, b + 1]]
        : [[node.id, b + 1], [other.id, b]]
      : [[node.id, b], [other.id, a]]
  sorting.value = true
  try {
    for (const [id, sort_order] of updates) {
      await updateTagTreeNode(props.kbId, id, { sort_order })
    }
    await loadTree()
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('knowledgeEditor.tagTree.operationFailed'))
  } finally {
    sorting.value = false
  }
}

function openMove(node: TagTreeNode) {
  movingNode.value = node
  moveTargetId.value = node.parent_id || ''
  moveVisible.value = true
}

async function handleMove() {
  const moving = movingNode.value
  if (!moving || !moveTargetValid.value) return
  saving.value = true
  try {
    await moveTagTreeNode(props.kbId, moving.id, { parent_id: moveTargetId.value })
    MessagePlugin.success(t('knowledgeEditor.tagTree.moveSuccess'))
    moveVisible.value = false
    await loadTree()
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('knowledgeEditor.tagTree.operationFailed'))
  } finally {
    saving.value = false
  }
}

function confirmDelete(node: TagTreeNode) {
  const dialog = DialogPlugin.confirm({
    header: t('knowledgeEditor.tagTree.deleteConfirm', { name: node.name }),
    body: t('knowledgeEditor.tagTree.deleteWarning'),
    theme: 'danger',
    confirmBtn: { content: t('knowledgeEditor.tagTree.delete'), theme: 'danger', loading: false },
    cancelBtn: { content: t('common.cancel') },
    onConfirm: async () => {
      dialog.update({ confirmBtn: { content: t('knowledgeEditor.tagTree.delete'), theme: 'danger', loading: true } })
      try {
        const res = await deleteTagTreeNode(props.kbId, node.id)
        const r = res.data
        MessagePlugin.success(
          t('knowledgeEditor.tagTree.deleted', {
            nodes: r.deleted_nodes,
            tags: r.deleted_tags,
            relations: r.removed_relations,
          }),
        )
        dialog.destroy()
        await loadTree()
      } catch (e: unknown) {
        dialog.update({ confirmBtn: { content: t('knowledgeEditor.tagTree.delete'), theme: 'danger', loading: false } })
        const message = e instanceof Error ? e.message : ''
        MessagePlugin.error(message || t('knowledgeEditor.tagTree.operationFailed'))
      }
    },
  })
}

watch(
  () => [props.kbId, props.active] as const,
  (val, old) => {
    const [kbId] = val
    // 初次挂载、切换 KB 或面板首次变为可见时加载；已是可见状态则不重复拉取
    if (kbId && (!old || old[0] !== kbId || !old[1])) loadTree()
  },
  { immediate: true },
)
</script>

<style scoped lang="less">
.kb-tag-tree-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}

.tag-tree-header {
  display: flex;
  align-items: center;
  gap: 10px;
}

.tag-tree-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.tag-tree-stats {
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.tag-tree-header-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 4px;
}

.tag-tree-desc {
  margin: 0;
  font-size: 13px;
  color: var(--td-text-color-secondary);
}

.tag-tree-body {
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--td-radius-medium);
  padding: 8px;
  min-height: 160px;
  max-height: 52vh;
  overflow: auto;
}

.tag-tree-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 32px 0;
  color: var(--td-text-color-secondary);
}

.tag-tree-empty {
  padding: 24px 0;
}

.tag-tree-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 6px;
  border-radius: var(--td-radius-default);
  margin-left: calc(var(--tag-tree-depth, 0) * 20px);

  &:hover {
    background: var(--td-bg-color-container-hover);

    .tag-tree-row-actions {
      opacity: 1;
    }
  }
}

.tag-tree-caret {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  background: transparent;
  color: var(--td-text-color-secondary);
  cursor: pointer;
  padding: 0;

  &.invisible {
    visibility: hidden;
    cursor: default;
  }
}

.tag-tree-name {
  font-size: 13px;
  color: var(--td-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 320px;
}

.tag-tree-row.is-leaf .tag-tree-name {
  font-weight: 500;
}

.tag-tree-row-actions {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 0;
  opacity: 0;
  transition: opacity 0.15s ease;
}

.tag-tree-rename-input {
  max-width: 320px;
}

.tag-tree-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 4px 0;
}

.tag-tree-form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.tag-tree-form-label {
  font-size: 13px;
  color: var(--td-text-color-secondary);
}

.tag-tree-demote-hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--td-radius-default);
  background: var(--td-warning-color-1);
  color: var(--td-warning-color);
  font-size: 12px;
  line-height: 1.5;
}
</style>
