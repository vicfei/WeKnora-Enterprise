<template>
  <!-- 标签树 2.0(v3.1.2):打标签用的树形选择面板。
       设计参照 TagTreePickerMenu(聊天输入的"按标签树筛选"),语义不同:
       这里是逐标签选择——点选任何携带标签的节点(含目录)只切换它自己的
       原生标签(选谁打谁),不做子树生效;无标签的节点是纯分组行。 -->
  <div class="tag-tree-select" role="tree">
    <div v-for="row in rows" :key="row.node.id" class="tag-tree-select-row"
      :style="{ '--tag-tree-select-depth': row.depth }" role="treeitem"
      :class="{ 'is-selectable': !!row.node.tag_id, 'is-selected': !!row.node.tag_id && selectedIds.has(row.node.tag_id) }"
      @click="row.node.tag_id && emit('toggle', row.node.tag_id)">
      <button type="button" class="tag-tree-select-caret" :class="{ invisible: !row.hasChildren }"
        :aria-expanded="expanded.has(row.node.id)" @click.stop="toggleExpand(row.node.id)">
        <t-icon :name="expanded.has(row.node.id) ? 'chevron-down' : 'chevron-right'" size="13px" />
      </button>
      <span v-if="row.node.tag_id" class="tag-tree-select-checkbox"
        :class="{ checked: selectedIds.has(row.node.tag_id) }">
        <svg v-if="selectedIds.has(row.node.tag_id)" width="10" height="10" viewBox="0 0 12 12" fill="none">
          <path d="M10 3L4.5 8.5L2 6" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </span>
      <span class="tag-tree-select-name" :title="row.node.path">{{ row.node.name }}</span>
      <span v-if="row.hasChildren" class="tag-tree-select-count">
        {{ $t('tagTreePicker.leafCount', { count: collectLeafTagIds(row.node).length }) }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { buildTagTreeRows, collectLeafTagIds } from './treeRows'
import type { TagTreeNode } from './api'

const props = defineProps<{
  nodes: TagTreeNode[]
  selectedIds: Set<string>
}>()

const emit = defineEmits<{ (e: 'toggle', tagId: string): void }>()

const { t } = useI18n()
const expanded = ref<Set<string>>(new Set())

// 树数据变化(重取/新建节点)时默认全展开,避免"看不到子标签"。
watch(
  () => props.nodes,
  (roots) => {
    const all = new Set<string>()
    const walk = (ns: TagTreeNode[]) => ns.forEach((n) => {
      if (n.children?.length) { all.add(n.id); walk(n.children) }
    })
    walk(roots || [])
    expanded.value = all
  },
  { immediate: true },
)

const rows = computed(() => buildTagTreeRows(props.nodes, expanded.value))

function toggleExpand(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

void t
</script>

<style scoped>
.tag-tree-select { max-height: 240px; overflow-y: auto; display: flex; flex-direction: column; gap: 2px; padding: 2px 0; }
.tag-tree-select-row {
  display: flex; align-items: center; gap: 6px; padding: 3px 6px; border-radius: 4px;
  padding-left: calc(6px + var(--tag-tree-select-depth, 0) * 16px); cursor: default; min-height: 26px;
}
.tag-tree-select-row.is-selectable { cursor: pointer; }
.tag-tree-select-row.is-selectable:hover { background: var(--td-bg-color-container-hover); }
.tag-tree-select-row.is-selected { background: var(--td-brand-color-light); }
.tag-tree-select-caret {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: none; background: transparent; cursor: pointer; padding: 0;
}
.tag-tree-select-caret-icon { color: var(--td-text-color-primary); flex: none; }
.tag-tree-select-caret.invisible { visibility: hidden; pointer-events: none; }
.tag-tree-select-checkbox {
  width: 14px; height: 14px; border: 1px solid var(--td-border-level-2-color); border-radius: 3px;
  display: inline-flex; align-items: center; justify-content: center; flex: none; background: var(--td-bg-color-container);
}
.tag-tree-select-checkbox.checked { background: var(--td-brand-color); border-color: var(--td-brand-color); }
.tag-tree-select-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.tag-tree-select-count { flex: none; font-size: 11px; color: var(--td-text-color-placeholder); }
</style>
