<template>
  <div v-if="visible" class="tag-tree-picker-overlay" @click="close" @wheel.stop>
    <div class="tag-tree-picker" :style="dropdownStyle" @click.stop>
      <div class="tag-tree-picker-header">
        <span class="tag-tree-picker-title">{{ $t('tagTreePicker.title') }}</span>
        <span v-if="selectedCount > 0" class="tag-tree-picker-count">{{ $t('tagTreePicker.selectedCount', { count: selectedCount }) }}</span>
      </div>

      <div class="tag-tree-picker-kb">
        <t-select v-model="currentKbId" size="small" :loading="false">
          <t-option v-for="kb in knowledgeBases" :key="kb.id" :value="kb.id" :label="kb.name" />
        </t-select>
      </div>

      <div class="tag-tree-picker-body">
        <div v-if="knowledgeBases.length === 0" class="tag-tree-picker-empty">
          {{ $t('tagTreePicker.noKnowledgeBases') }}
        </div>
        <div v-else-if="loading" class="tag-tree-picker-empty">
          <t-loading size="small" />
        </div>
        <div v-else-if="rows.length === 0" class="tag-tree-picker-empty">
          {{ $t('tagTreePicker.empty') }}
        </div>
        <template v-else>
          <div v-for="row in rows" :key="row.node.id" class="tag-tree-picker-row"
            :style="{ '--tag-tree-picker-depth': row.depth }" role="treeitem"
            :class="{ 'is-disabled': !row.node.is_leaf && !rowHasAnyLeaf(row.node), 'is-selected': isEffectivelySelected(row.node) }"
            @click="toggleNode(row.node)">
            <button type="button" class="tag-tree-picker-caret" :class="{ invisible: !row.hasChildren }"
              :aria-expanded="expanded.has(row.node.id)" @click.stop="toggleExpand(row.node.id)">
              <t-icon :name="expanded.has(row.node.id) ? 'chevron-down' : 'chevron-right'" size="13px" />
            </button>
            <span class="tag-tree-picker-checkbox" :class="{
              checked: isEffectivelySelected(row.node),
              disabled: !row.node.is_leaf && !rowHasAnyLeaf(row.node),
            }">
              <svg v-if="isEffectivelySelected(row.node)" width="10" height="10" viewBox="0 0 12 12" fill="none">
                <path d="M10 3L4.5 8.5L2 6" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </span>
            <span class="tag-tree-picker-name" :title="row.node.path">{{ row.node.name }}</span>
            <span v-if="!row.node.is_leaf" class="tag-tree-picker-leaf-count">
              {{ rowHasAnyLeaf(row.node) ? $t('tagTreePicker.leafCount', { count: collectLeafTagIds(row.node).length }) : $t('tagTreePicker.noLeaves') }}
            </span>
          </div>
        </template>
      </div>

      <div class="tag-tree-picker-actions">
        <button type="button" class="tag-tree-picker-btn" :disabled="selectedNodeIds.size === 0" @click="clearSelection">
          {{ $t('common.clear') }}
        </button>
        <button type="button" class="tag-tree-picker-btn primary" :disabled="selectedNodeIds.size === 0 || !currentKb"
          @click="confirmSelection">
          {{ $t('common.confirm') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { getTagTree, type TagTreeNode } from './api'
import {
  buildTagTreeRows,
  collectLeafTagIds,
  collectLeafTags,
  findTagTreeNode,
  nodeHasAnyLeaf,
  pruneSelectionToTopmost,
  tagTreeNodeAncestors,
} from './treeRows'

const { t } = useI18n()

/**
 * One confirmed picker selection: either a leaf (single native tag) or a
 * group node standing for every leaf beneath it. The host turns each entry
 * into one aggregated chip; group chips carry their leaf tags so the send
 * path can expand them into per-leaf mentions.
 */
export type TagTreeSelection = {
  nodeId: string
  name: string
  isGroup: boolean
  leafTags: Array<{ id: string; name: string }>
}

const props = defineProps<{
  visible: boolean
  anchorEl?: any | null
  knowledgeBases: Array<{ id: string; name: string }>
  defaultKbId?: string
  dropdownWidth?: number
  offsetY?: number
}>()

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'close'): void
  (e: 'select', payload: { kbId: string; kbName: string; items: TagTreeSelection[] }): void
}>()

const dropdownWidth = props.dropdownWidth ?? 320
const offsetY = props.offsetY ?? 8

const currentKbId = ref('')
const tree = ref<TagTreeNode[]>([])
const loading = ref(false)
const expanded = ref(new Set<string>())
const selectedNodeIds = ref(new Set<string>())
const dropdownStyle = ref<Record<string, string>>({})

const currentKb = computed(() => props.knowledgeBases.find((kb) => kb.id === currentKbId.value))
const rows = computed(() => buildTagTreeRows(tree.value, expanded.value))
const selectedCount = computed(() => selectedNodeIds.value.size)

function rowHasAnyLeaf(node: TagTreeNode): boolean {
  return nodeHasAnyLeaf(node)
}

function isEffectivelySelected(node: TagTreeNode): boolean {
  if (selectedNodeIds.value.has(node.id)) return true
  return tagTreeNodeAncestors(tree.value, node.id).some((id) => selectedNodeIds.value.has(id))
}

function toggleExpand(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

function descendantIds(node: TagTreeNode): string[] {
  const ids: string[] = []
  const walk = (n: TagTreeNode) => {
    n.children?.forEach((child) => {
      ids.push(child.id)
      walk(child)
    })
  }
  walk(node)
  return ids
}

function toggleNode(node: TagTreeNode) {
  // Red line: a group without any leaf cannot filter anything — never let it
  // into the selection (an empty tag list means "no results", not "no filter").
  if (!node.is_leaf && !nodeHasAnyLeaf(node)) return
  const next = new Set(selectedNodeIds.value)
  if (isEffectivelySelected(node)) {
    next.delete(node.id)
    descendantIds(node).forEach((id) => next.delete(id))
  } else {
    next.add(node.id)
    descendantIds(node).forEach((id) => next.delete(id))
  }
  selectedNodeIds.value = next
}

function clearSelection() {
  selectedNodeIds.value = new Set()
}

function confirmSelection() {
  const kb = currentKb.value
  if (!kb) return
  const topmost = pruneSelectionToTopmost(tree.value, [...selectedNodeIds.value])
  const items: TagTreeSelection[] = topmost
    .map((id) => findTagTreeNode(tree.value, id))
    .filter((node): node is TagTreeNode => !!node)
    .map((node) => ({
      nodeId: node.id,
      name: node.name,
      isGroup: !node.is_leaf,
      leafTags: collectLeafTags(node),
    }))
    .filter((item) => item.leafTags.length > 0)
  if (items.length === 0) return
  emit('select', { kbId: kb.id, kbName: kb.name, items })
  selectedNodeIds.value = new Set()
  close()
}

function close() {
  emit('update:visible', false)
  emit('close')
}

async function loadTree() {
  if (!currentKbId.value) return
  loading.value = true
  tree.value = []
  try {
    const res = await getTagTree(currentKbId.value)
    tree.value = Array.isArray(res.data) ? res.data : []
    // Auto-expand the first level so the tree does not look empty at a glance.
    expanded.value = new Set(tree.value.filter((n) => n.children?.length).map((n) => n.id))
  } catch (e: unknown) {
    const message = e instanceof Error ? e.message : ''
    MessagePlugin.error(message || t('tagTreePicker.loadFailed'))
  } finally {
    loading.value = false
  }
}

const resolveAnchorEl = (): HTMLElement | null => {
  const a = props.anchorEl
  if (!a) return null
  if (typeof a === 'object' && 'value' in a) return (a.value as HTMLElement) ?? null
  if (typeof a === 'object' && '$el' in a) return (a as any).$el ?? null
  return a as HTMLElement
}

function updateDropdownPosition() {
  const anchor = resolveAnchorEl()
  if (!anchor) {
    dropdownStyle.value = {}
    return
  }
  const rect = anchor.getBoundingClientRect()
  const width = Math.min(dropdownWidth, window.innerWidth - 16)
  let left = rect.left
  if (left + width > window.innerWidth - 8) left = Math.max(8, window.innerWidth - 8 - width)
  const style: Record<string, string> = {
    position: 'fixed',
    left: `${Math.round(left)}px`,
    width: `${width}px`,
    zIndex: '2600',
  }
  const height = Math.min(420, window.innerHeight * 0.6)
  const spaceBelow = window.innerHeight - rect.bottom
  if (spaceBelow > height + offsetY + 8 || spaceBelow > window.innerHeight / 2) {
    style.top = `${Math.round(rect.bottom + offsetY)}px`
  } else {
    style.top = `${Math.max(8, Math.round(rect.top - offsetY - height))}px`
  }
  dropdownStyle.value = style
}

watch(
  () => props.visible,
  (visible) => {
    if (visible) {
      selectedNodeIds.value = new Set()
      if (!currentKbId.value) {
        currentKbId.value =
          props.defaultKbId ||
          (props.knowledgeBases.find((kb) => kb.id === props.defaultKbId)?.id ?? '') ||
          props.knowledgeBases[0]?.id ||
          ''
      }
      nextTick(updateDropdownPosition)
    }
  },
)

watch(currentKbId, () => {
  selectedNodeIds.value = new Set()
  loadTree()
})

watch(
  () => props.defaultKbId,
  (kbId) => {
    if (props.visible && kbId && kbId !== currentKbId.value) currentKbId.value = kbId
  },
)
</script>

<style scoped lang="less">
.tag-tree-picker-overlay {
  position: fixed;
  inset: 0;
  z-index: 2500;
}

.tag-tree-picker {
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--td-radius-medium);
  box-shadow: var(--td-shadow-2);
  overflow: hidden;
}

.tag-tree-picker-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px 6px;
}

.tag-tree-picker-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.tag-tree-picker-count {
  font-size: 12px;
  color: var(--td-brand-color);
}

.tag-tree-picker-kb {
  padding: 0 12px 8px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.tag-tree-picker-body {
  max-height: 300px;
  overflow: auto;
  padding: 6px;
}

.tag-tree-picker-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 80px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.tag-tree-picker-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border-radius: var(--td-radius-default);
  margin-left: calc(var(--tag-tree-picker-depth, 0) * 18px);
  cursor: pointer;
  user-select: none;

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &.is-disabled {
    cursor: not-allowed;
    opacity: 0.55;

    &:hover {
      background: transparent;
    }
  }
}

.tag-tree-picker-caret {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border: none;
  background: transparent;
  color: var(--td-text-color-secondary);
  padding: 0;
  cursor: pointer;

  &.invisible {
    visibility: hidden;
  }
}

.tag-tree-picker-checkbox {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 3px;
  background: var(--td-bg-color-container);
  flex-shrink: 0;

  &.checked {
    background: var(--td-brand-color);
    border-color: var(--td-brand-color);
  }

  &.disabled {
    background: var(--td-bg-color-component-disabled);
  }
}

.tag-tree-picker-name {
  font-size: 13px;
  color: var(--td-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 180px;
}

.tag-tree-picker-row.is-selected .tag-tree-picker-name {
  color: var(--td-brand-color);
  font-weight: 500;
}

.tag-tree-picker-leaf-count {
  margin-left: auto;
  font-size: 11px;
  color: var(--td-text-color-placeholder);
  white-space: nowrap;
}

.tag-tree-picker-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 8px 12px;
  border-top: 1px solid var(--td-component-stroke);
}

.tag-tree-picker-btn {
  border: 1px solid var(--td-component-stroke);
  background: transparent;
  border-radius: var(--td-radius-default);
  padding: 3px 12px;
  font-size: 12px;
  color: var(--td-text-color-primary);
  cursor: pointer;

  &:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  &.primary {
    background: var(--td-brand-color);
    border-color: var(--td-brand-color);
    color: #fff;
  }
}
</style>
