<template>
  <div class="feedback-admin">
    <div class="feedback-filters">
      <t-select v-model="filterRating" clearable :placeholder="t('system.feedback.filterRating')" style="width: 140px"
        :options="[
          { label: t('system.feedback.up'), value: 'up' },
          { label: t('system.feedback.down'), value: 'down' },
        ]" @change="reload" />
      <t-select v-model="filterReason" clearable :placeholder="t('system.feedback.filterReason')" style="width: 200px"
        :options="reasonOptions" @change="reload" />
      <t-button variant="outline" @click="reload">{{ t('common.refresh') }}</t-button>
    </div>
    <t-table
      row-key="id"
      :data="rows"
      :columns="columns"
      :loading="loading"
      :pagination="{ current: page, pageSize, total, onChange: onPage }"
      size="small"
      hover
      stripe
    >
      <template #rating="{ row }">
        <t-tag :theme="row.rating === 'up' ? 'success' : 'danger'" size="small">
          {{ row.rating === 'up' ? t('system.feedback.up') : t('system.feedback.down') }}
        </t-tag>
      </template>
      <template #reason="{ row }">
        {{ reasonLabel(row.reason_code) }}
      </template>
    </t-table>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { useI18n } from 'vue-i18n';
import { listMessageFeedback } from '@/api/knowledge-base';

const { t } = useI18n();
const rows = ref<any[]>([]);
const loading = ref(false);
const page = ref(1);
const pageSize = 20;
const total = ref(0);
const filterRating = ref('');
const filterReason = ref('');

const reasonKeys = ['wrong_answer', 'bad_citation', 'outdated', 'incomplete', 'style', 'other'];
const reasonOptions = computed(() =>
  reasonKeys.map(k => ({ label: t(`chat.feedback.reason.${k}`), value: k })));

const columns = computed(() => [
  { colKey: 'created_at', title: t('system.feedback.columns.time'), width: 150 },
  { colKey: 'rating', title: t('system.feedback.columns.rating'), width: 80 },
  { colKey: 'reason', title: t('system.feedback.columns.reason'), width: 130 },
  { colKey: 'question', title: t('system.feedback.columns.question'), ellipsis: true },
  { colKey: 'answer_excerpt', title: t('system.feedback.columns.answer'), ellipsis: true },
  { colKey: 'comment', title: t('system.feedback.columns.comment'), ellipsis: true },
]);

const reasonLabel = (code: string) =>
  code ? t(`chat.feedback.reason.${code}`, code) : '-';

const fmt = (row: any) => {
  const snap = row.snapshot || {};
  return { ...row, question: snap.question || '', answer_excerpt: snap.answer_excerpt || '' };
};

const reload = async () => {
  loading.value = true;
  try {
    const res: any = await listMessageFeedback({
      rating: filterRating.value || undefined,
      reason_code: filterReason.value || undefined,
      page: page.value,
      page_size: pageSize,
    });
    rows.value = (res?.data || []).map(fmt);
    total.value = res?.total || 0;
  } finally {
    loading.value = false;
  }
};

const onPage = (p: number) => { page.value = p; reload(); };
onMounted(reload);
</script>

<style scoped>
.feedback-admin { display: flex; flex-direction: column; gap: 12px; }
.feedback-filters { display: flex; gap: 8px; align-items: center; }
</style>
