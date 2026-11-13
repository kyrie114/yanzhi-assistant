<script setup lang="ts">
import { computed } from 'vue';

const props = defineProps<{
  status: string;
}>();

const badgeClass = computed(() => {
  switch (props.status) {
    case '已完成':
      return 'badge badge-success';
    case '失败':
      return 'badge badge-danger';
    case '解析中':
    case '向量化中':
      return 'badge badge-info';
    default:
      return 'badge badge-warn';
  }
});
</script>

<template>
  <span :class="badgeClass">{{ status }}</span>
</template>
