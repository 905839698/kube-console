<template>
  <el-table :data="events" size="small" stripe max-height="480">
    <el-table-column label="类型" width="80">
      <template #default="{ row }">
        <el-tag :type="row.type === 'Normal' ? 'info' : 'danger'" size="small">{{ row.type }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column prop="reason" label="原因" width="150" />
    <el-table-column prop="message" label="消息" min-width="300" show-overflow-tooltip />
    <el-table-column prop="object" label="对象" width="180" />
    <el-table-column prop="count" label="次数" width="70" align="center" />
    <el-table-column prop="lastSeen" label="最近发生" width="160" />
  </el-table>
</template>

<script setup lang="ts">
import type { EventItem } from '../api'

defineProps<{ events: EventItem[] }>()
</script>
