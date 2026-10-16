<!-- 备份概览（Velero）：备份任务 / 备份计划 / 恢复任务的可视化。
     进度、耗时、快照数、失败信息（warnings + 关联事件）来自 velero CR 的 status；
     写操作（发起恢复 / 启停计划 / 删除）仅平台管理员——恢复会重建整个命名空间的
     资源，删除备份连带对象存储里的数据。列表 10s 自动轮询仅在有进行中任务时开启。 -->
<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>备份概览（Velero）</span>
        <div class="hd-actions">
          <el-select v-model="clusterName" style="width: 200px" @change="onClusterChange">
            <el-option v-for="cl in clusters" :key="cl" :label="cl" :value="cl" />
          </el-select>
          <el-button v-if="installed" type="primary" size="small" style="margin-left: 8px"
            :disabled="!canAdmin || !canCreate" @click="openBackupDlg">新建备份</el-button>
          <el-button v-if="installed" size="small" :disabled="!canAdmin || !canCreate" @click="openScheduleDlg">新建计划</el-button>
          <el-tag v-if="polling" size="small" type="primary" class="poll-tag" style="margin-left: 8px">有进行中的任务，10s 自动刷新</el-tag>
          <el-button :icon="Refresh" circle size="small" :loading="loading" @click="load" style="margin-left: 8px" />
        </div>
      </div>
    </template>

    <el-alert v-if="!installed && !loading" type="info" :closable="false" style="margin-bottom: 12px"
      title="该集群未安装 Velero（未发现 backups.velero.io CRD）。部署 Velero 并配置 BackupStorageLocation 后，备份任务 / 计划 / 恢复将在此汇聚展示。" />

    <template v-if="installed">
      <el-alert v-if="partial.length" type="warning" :closable="false" style="margin-bottom: 12px"
        :title="'部分数据读取失败（多为该集群 kubeconfig 对 Velero CRD 的 RBAC 不足）：' + partial.join('；')" />
      <!-- locations===undefined 是后端版本过旧（未返回该字段），不能谎称"集群没有存储位置" -->
      <el-alert v-if="locationsMissing" type="warning" :closable="false" style="margin-bottom: 12px"
        title="后端接口未返回存储位置列表（服务端版本过旧）。请用最新 master 重新构建部署 kube-console-server 后刷新本页。" />
      <el-alert v-else-if="!locations?.length && !partial.length" type="warning" :closable="false" style="margin-bottom: 12px"
        title="集群里没有 BackupStorageLocation（备份目的地）：可浏览历史任务，但新建备份/计划/恢复不可用。请先在 Velero 配置存储位置。" />

      <div class="stat-row">
        <div class="stat-card"><div class="stat-num">{{ backups.length }}</div><div class="stat-label">备份任务</div></div>
        <div class="stat-card ok"><div class="stat-num">{{ countByPhase(backups, ['Completed']) }}</div><div class="stat-label">成功</div></div>
        <div class="stat-card warn"><div class="stat-num">{{ countByPhase(backups, ['PartiallyFailed']) }}</div><div class="stat-label">部分失败</div></div>
        <div class="stat-card bad"><div class="stat-num">{{ countByPhase(backups, ['Failed']) }}</div><div class="stat-label">失败</div></div>
        <div class="stat-card"><div class="stat-num">{{ activeCount }}</div><div class="stat-label">进行中</div></div>
        <div class="stat-card"><div class="stat-num">{{ schedules.length }}</div><div class="stat-label">计划（{{ pausedCount }} 暂停）</div></div>
      </div>

      <!-- ============ 备份任务 ============ -->
      <div class="sec-title">备份任务</div>
      <el-table border :data="backups" size="small" stripe>
        <el-table-column label="名称" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link type="primary" :underline="false" @click="openDetail('Backup', row)">{{ row.name }}</el-link>
            <span class="dim"> @{{ row.namespace }}</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ row.schedule || '手动' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="110" align="center">
          <template #default="{ row }"><PhaseTag :phase="row.phase" /></template>
        </el-table-column>
        <el-table-column label="进度" min-width="150">
          <template #default="{ row }">
            <el-progress v-if="row.progressTotal > 0" :percentage="pct(row.progressCompleted, row.progressTotal)"
              :status="progressStatus(row.phase)" :stroke-width="10" text-inside />
            <span v-else class="dim">{{ isActivePhase(row.phase) ? '等待调度' : '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="快照" width="80" align="center">
          <template #default="{ row }">
            <span v-if="row.snapshotsAttempted > 0" :class="{ 'txt-warn': row.snapshotsCaptured < row.snapshotsAttempted }">
              {{ row.snapshotsCaptured }}/{{ row.snapshotsAttempted }}
            </span>
            <span v-else class="dim">—</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="80" align="center">
          <template #default="{ row }">{{ fmtDurationSec(row.durationSec) }}</template>
        </el-table-column>
        <el-table-column label="位置 / 到期" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            <div>{{ row.storageLocation || '—' }}</div>
            <div class="dim">到期 {{ fmtTime(row.expiration) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="150">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" plain :disabled="!canAdmin || !restorable(row)" @click="openRestore(row)">恢复</el-button>
            <el-button size="small" text @click="openDetail('Backup', row)">详情</el-button>
            <el-button size="small" text type="danger" :disabled="!canAdmin" @click="delBackup(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- ============ 备份计划 ============ -->
      <div class="sec-title">备份计划 ({{ schedules.length }})</div>
      <el-table border :data="schedules" size="small" stripe>
        <el-table-column label="名称" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <span>{{ row.name }}</span><span class="dim"> @{{ row.namespace }}</span>
          </template>
        </el-table-column>
        <el-table-column label="Cron" width="140">
          <template #default="{ row }"><span class="mono">{{ row.cron }}</span></template>
        </el-table-column>
        <el-table-column label="下次执行" width="150">
          <template #default="{ row }">
            <span v-if="row.paused" class="dim">已暂停</span>
            <span v-else>{{ fmtTime(row.nextRun) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="上次备份" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <div>{{ row.lastBackup || '尚未执行' }}</div>
            <div v-if="row.lastError" class="txt-warn">{{ row.lastError }}</div>
          </template>
        </el-table-column>
        <el-table-column label="模板摘要" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span>{{ nsSummary(row.includedNamespaces, row.excludedNamespaces) }}</span>
            <el-tag size="small" :type="row.snapshotVolumes ? 'success' : 'info'" class="tpl-tag">{{ row.snapshotVolumes ? '含快照' : '不含快照' }}</el-tag>
            <el-tag size="small" :type="row.fsBackup ? 'warning' : 'info'" class="tpl-tag">{{ row.fsBackup ? '卷文件备份' : '不备卷文件' }}</el-tag>
            <span class="dim">{{ row.ttl ? '保留 ' + row.ttl : '' }} {{ row.storageLocation || '' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70" align="center">
          <template #default="{ row }">
            <el-switch :model-value="!row.paused" :disabled="!canAdmin" @change="(v: any) => setPaused(row, !!v)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="120" align="center" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text type="primary" :disabled="!canAdmin" @click="openEditScheduleDlg(row)">编辑</el-button>
            <el-button size="small" text type="danger" :disabled="!canAdmin" @click="delSchedule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- ============ 恢复任务 ============ -->
      <div class="sec-title">恢复任务 ({{ restores.length }})</div>
      <el-table border :data="restores" size="small" stripe>
        <el-table-column label="名称" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link type="primary" :underline="false" @click="openDetail('Restore', row)">{{ row.name }}</el-link>
            <span class="dim"> @{{ row.namespace }}</span>
          </template>
        </el-table-column>
        <el-table-column label="源备份" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">{{ row.backupName }}</template>
        </el-table-column>
        <el-table-column label="状态" width="110" align="center">
          <template #default="{ row }"><PhaseTag :phase="row.phase" /></template>
        </el-table-column>
        <el-table-column label="进度" min-width="150">
          <template #default="{ row }">
            <el-progress v-if="row.progressTotal > 0" :percentage="pct(row.progressCompleted, row.progressTotal)"
              :status="progressStatus(row.phase)" :stroke-width="10" text-inside />
            <span v-else class="dim">{{ isActivePhase(row.phase) ? '等待调度' : '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="80" align="center">
          <template #default="{ row }">{{ fmtDurationSec(row.durationSec) }}</template>
        </el-table-column>
        <el-table-column label="创建时间" width="150">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button size="small" text @click="openDetail('Restore', row)">详情</el-button>
            <el-button size="small" text type="danger" :disabled="!canAdmin" @click="delRestore(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </template>
  </el-card>

  <!-- 详情抽屉：状态字段 + 失败信息 + 关联事件（资源级错误明细在备份 tarball 内，
       控制台不解析对象存储，这里给事件与计数） -->
  <el-drawer v-model="detailVisible" size="58%" :title="detailKind + ' 详情：' + (detailItem?.name || '')">
    <div v-if="detailItem" class="detail">
      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="命名空间">{{ detailItem.namespace }}</el-descriptions-item>
        <el-descriptions-item label="状态"><PhaseTag :phase="detailItem.phase" /></el-descriptions-item>
        <el-descriptions-item v-if="detailKind === 'Backup'" label="来源计划">{{ (detailItem as VeleroBackup).schedule || '手动' }}</el-descriptions-item>
        <el-descriptions-item v-if="detailKind === 'Restore'" label="源备份">{{ (detailItem as VeleroRestore).backupName }}</el-descriptions-item>
        <el-descriptions-item label="开始">{{ fmtTime(detailItem.startedAt) }}</el-descriptions-item>
        <el-descriptions-item label="结束">{{ fmtTime(detailItem.finishedAt) }}</el-descriptions-item>
        <el-descriptions-item label="进度">{{ (detailItem as any).progressCompleted }} / {{ (detailItem as any).progressTotal }}</el-descriptions-item>
        <el-descriptions-item label="耗时">{{ fmtDurationSec(detailItem.durationSec) }}</el-descriptions-item>
        <el-descriptions-item v-if="detailKind === 'Backup'" label="快照" :span="2">
          {{ (detailItem as VeleroBackup).snapshotsCaptured }}/{{ (detailItem as VeleroBackup).snapshotsAttempted }}
          · 快照 {{ (detailItem as VeleroBackup).snapshotVolumes ? '开' : '关' }}
          · 卷文件备份 {{ (detailItem as VeleroBackup).fsBackup ? '开（node-agent 上传卷内容）' : '关' }}
        </el-descriptions-item>
        <el-descriptions-item label="命名空间范围" :span="2">
          {{ detailKind === 'Restore'
            ? nsSummary((detailItem as VeleroRestore).includedNamespaces)
            : nsSummary((detailItem as VeleroBackup).includedNamespaces, (detailItem as VeleroBackup).excludedNamespaces) }}
        </el-descriptions-item>
      </el-descriptions>

      <template v-if="detailWarnings.length">
        <div class="sec-title">警告 / 错误（{{ detailWarnings.length }}）</div>
        <ul class="warn-list">
          <li v-for="(w, i) in detailWarnings" :key="i">{{ w }}</li>
        </ul>
      </template>
      <div v-else-if="isActivePhase(detailItem.phase)" class="dim">进行中，暂无错误。</div>

      <template v-if="detailKind === 'Backup'">
        <div class="sec-title">
          卷备份明细
          <span v-if="volumeFailures.length" class="fail-hint">（{{ volumeFailures.length }} 个卷未完成——「部分失败」即来自这里）</span>
          <span v-else-if="!volumesLoading && volumes.length" class="dim">（{{ volumes.length }} 个卷全部完成）</span>
        </div>
        <el-table v-if="volumeFailures.length || volumes.length > 20" border :data="volumeFailures.length ? volumeFailures : volumes" size="small" v-loading="volumesLoading" max-height="260">
          <el-table-column label="Pod" min-width="220">
            <template #default="{ row }"><span class="mono">{{ row.podNamespace }}/{{ row.podName }}</span></template>
          </el-table-column>
          <el-table-column prop="volume" label="卷" width="110" />
          <el-table-column label="状态" width="110">
            <template #default="{ row }"><el-tag size="small" :type="row.phase === 'Completed' ? 'success' : 'danger'" effect="dark">{{ row.phase || '-' }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="node" label="节点" width="170" show-overflow-tooltip />
          <el-table-column prop="message" label="信息" min-width="200" show-overflow-tooltip />
        </el-table>
        <div v-else-if="!volumesLoading && !volumes.length" class="dim">无卷备份记录（该备份未包含需文件级备份的卷，或集群未启用 node-agent）。</div>
        <div class="dim" style="margin-top: 4px">资源级错误明细（具体文件）保存在对象存储的备份包内，如需可执行：velero backup describe {{ detailItem.name }} --details</div>
      </template>

      <div class="sec-title">关联事件</div>
      <el-table border :data="events" size="small" v-loading="eventsLoading">
        <el-table-column prop="type" label="类型" width="80" align="center">
          <template #default="{ row }"><el-tag size="small" :type="row.type === 'Warning' ? 'warning' : 'info'">{{ row.type }}</el-tag></template>
        </el-table-column>
        <el-table-column prop="reason" label="原因" width="150" show-overflow-tooltip />
        <el-table-column prop="message" label="消息" min-width="260" show-overflow-tooltip />
        <el-table-column prop="count" label="次数" width="60" align="center" />
        <el-table-column prop="lastSeen" label="最近" width="150" />
      </el-table>
      <div v-if="!eventsLoading && !events.length" class="dim">无事件（Velero controller 未对该对象发出事件，或已过事件保留期）。</div>
    </div>
  </el-drawer>

  <!-- 新建备份（手动一次） -->
  <el-dialog v-model="backupDlg" width="560px" title="新建备份（立即执行一次）">
    <el-form label-width="110px" size="small">
      <el-form-item label="名称">
        <el-input v-model="backupForm.name" placeholder="留空自动生成 manual-<时间戳>" />
      </el-form-item>
      <el-form-item label="命名空间">
        <el-select v-model="tpl.includedNamespaces" multiple filterable clearable style="width: 100%"
          placeholder="留空 = 全集群范围；选择则只备份这些命名空间">
          <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
        </el-select>
      </el-form-item>
      <el-form-item label="排除命名空间">
        <el-select v-model="tpl.excludedNamespaces" multiple filterable clearable style="width: 100%" placeholder="可多选/手输">
          <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
        </el-select>
      </el-form-item>
      <el-form-item label="PV 快照">
        <el-switch v-model="tpl.snapshotVolumes" />
        <span class="dim" style="margin-left: 8px">只控制是否调用存储层快照；卷内容是否进备份还看下面的「卷文件备份」</span>
      </el-form-item>
      <el-form-item label="卷文件备份">
        <el-switch v-model="tpl.fsBackup" />
        <span class="dim" style="margin-left: 8px">开启后 node-agent 把卷内文件打包上传对象存储（defaultVolumesToFsBackup）；快照与文件备份都关 = 不保护卷数据</span>
      </el-form-item>
      <el-form-item label="存储位置">
        <el-select v-model="tpl.storageLocation" clearable style="width: 100%" placeholder="留空 = 默认位置">
          <el-option v-for="l in locations" :key="l.name" :label="l.name + (l.default ? '（默认）' : '')" :value="l.name" />
        </el-select>
      </el-form-item>
      <el-form-item label="保留期(小时)">
        <el-input-number v-model="tpl.ttlHours" :min="0" :max="87600" controls-position="right" style="width: 160px" />
        <span class="dim" style="margin-left: 8px">0 = Velero 默认 720h（30 天）</span>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="backupDlg = false">取消</el-button>
      <el-button type="primary" :loading="creating" @click="doCreateBackup">发起备份</el-button>
    </template>
  </el-dialog>

  <!-- 新建/编辑备份计划 -->
  <el-dialog v-model="scheduleDlg" width="560px" :title="editingSchedule ? '编辑备份计划' : '新建备份计划'">
    <el-form label-width="110px" size="small">
      <el-form-item label="名称" required>
        <el-input v-model="scheduleForm.name" :disabled="!!editingSchedule" placeholder="如 nightly-full（小写字母/数字/-）" />
      </el-form-item>
      <el-form-item label="Cron" required>
        <el-input v-model="scheduleForm.cron" placeholder="标准 5 段，如 0 1 * * *（每天 01:00）或 0 */6 * * *（每 6 小时）" />
      </el-form-item>
      <el-form-item label="创建后暂停">
        <el-switch v-model="scheduleForm.paused" />
        <span class="dim" style="margin-left: 8px">开着 = 先建好计划不生效</span>
      </el-form-item>
      <el-divider style="margin: 6px 0" />
      <el-form-item label="命名空间">
        <el-select v-model="tpl.includedNamespaces" multiple filterable clearable style="width: 100%"
          placeholder="留空 = 全集群范围">
          <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
        </el-select>
      </el-form-item>
      <el-form-item label="排除命名空间">
        <el-select v-model="tpl.excludedNamespaces" multiple filterable clearable style="width: 100%" placeholder="可多选/手输">
          <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
        </el-select>
      </el-form-item>
      <el-form-item label="PV 快照">
        <el-switch v-model="tpl.snapshotVolumes" />
      </el-form-item>
      <el-form-item label="卷文件备份">
        <el-switch v-model="tpl.fsBackup" />
        <span class="dim" style="margin-left: 8px">node-agent 文件级备份卷内容；两者都关 = 只备资源定义</span>
      </el-form-item>
      <el-form-item label="存储位置">
        <el-select v-model="tpl.storageLocation" clearable style="width: 100%" placeholder="留空 = 默认位置">
          <el-option v-for="l in locations" :key="l.name" :label="l.name + (l.default ? '（默认）' : '')" :value="l.name" />
        </el-select>
      </el-form-item>
      <el-form-item label="保留期(小时)">
        <el-input-number v-model="tpl.ttlHours" :min="0" :max="87600" controls-position="right" style="width: 160px" />
        <span class="dim" style="margin-left: 8px">0 = Velero 默认 720h</span>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="scheduleDlg = false">取消</el-button>
      <el-button type="primary" :loading="creating" @click="doCreateSchedule">{{ editingSchedule ? '保存' : '创建计划' }}</el-button>
    </template>
  </el-dialog>

  <!-- 新建恢复 -->
  <el-dialog v-model="restoreDlg" width="560px" title="从备份恢复">
    <template v-if="restoreTarget">
      <el-descriptions :column="1" border size="small" style="margin-bottom: 12px">
        <el-descriptions-item label="源备份">{{ restoreTarget.name }}（{{ restoreTarget.namespace }}）</el-descriptions-item>
        <el-descriptions-item label="范围">{{ nsSummary(restoreTarget.includedNamespaces, restoreTarget.excludedNamespaces) }} · {{ restoreTarget.storageLocation }}</el-descriptions-item>
      </el-descriptions>
      <el-form label-width="110px" size="small">
        <el-form-item label="恢复名称">
          <el-input v-model="restoreForm.name" :placeholder="'留空自动生成 <备份名>-rst-<时间戳>'" />
        </el-form-item>
        <el-form-item label="命名空间">
          <el-select v-model="restoreForm.includedNamespaces" multiple filterable clearable style="width: 100%"
            placeholder="留空 = 按备份原样全量恢复；可选单个 ns 恢复（重名覆盖需注意 Velero 恢复语义）">
            <el-option v-for="n in (restoreTarget.includedNamespaces || [])" :key="n" :label="n" :value="n" />
          </el-select>
        </el-form-item>
        <el-form-item label="恢复 PV">
          <el-switch v-model="restoreForm.restorePVs" />
          <span class="dim" style="margin-left: 8px">关闭则只恢复资源定义，不重建 PV/PVC 绑定</span>
        </el-form-item>
        <el-form-item label="保留 NodePort">
          <el-switch v-model="restoreForm.preserveNodePorts" />
        </el-form-item>
      </el-form>
      <el-alert type="warning" :closable="false" show-icon
        title="恢复会把备份里的资源重新创建到集群：同名资源按 Velero 语义跳过或覆盖，请先确认目标环境状态。" />
    </template>
    <template #footer>
      <el-button @click="restoreDlg = false">取消</el-button>
      <el-button type="primary" :loading="restoring" @click="doRestore">发起恢复</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import {
  veleroApi, clusterApi, k8sApi,
  type VeleroBackup, type VeleroSchedule, type VeleroRestore, type VeleroEvent, type VeleroLocation,
  type VeleroVolumeBackup,
} from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'
import { usePerm } from '../store/perm'
import { confirmDelete } from '../utils/confirm'

const clusterStore = useClusterStore()
const userStore = useUserStore()
const perm = usePerm()
// 写门控：发起恢复/启停/删除 = 平台管理员（与后端 admin 组一致）
const canAdmin = computed(() => !!perm.access.value.platformAdmin || userStore.isAdmin)

const clusters = ref<string[]>([])
const clusterName = ref('')
const loading = ref(false)
const installed = ref(false)
const partial = ref<string[]>([])
// null = 后端未返回该字段（版本过旧）；[] = 确实没配置 BSL——两种情况提示不同
const locations = ref<VeleroLocation[] | null>(null)
const veleroNs = ref('')
const locationsMissing = computed(() => installed.value && locations.value === null)
const canCreate = computed(() => !!locations.value?.length)
const backups = ref<VeleroBackup[]>([])
const schedules = ref<VeleroSchedule[]>([])
const restores = ref<VeleroRestore[]>([])

const activePhases = ['New', 'InProgress']
const isActivePhase = (p?: string) => !!p && activePhases.includes(p)
const restorable = (b: VeleroBackup) => ['Completed', 'PartiallyFailed'].includes(b.phase)

const activeCount = computed(() =>
  backups.value.filter((b) => isActivePhase(b.phase)).length + restores.value.filter((r) => isActivePhase(r.phase)).length)
const pausedCount = computed(() => schedules.value.filter((s) => s.paused).length)
function countByPhase(list: { phase?: string }[], phases: string[]) {
  return list.filter((x) => phases.includes(x.phase || '')).length
}

// ---- 轮询：仅在有进行中任务时 10s 刷新 ----
const polling = ref(false)
let timer: ReturnType<typeof setInterval> | null = null
function syncPoll() {
  const need = activeCount.value > 0
  if (need === polling.value) return
  polling.value = need
  if (need) {
    timer = setInterval(() => { void load(true) }, 10000)
  } else if (timer) {
    clearInterval(timer); timer = null
  }
}

async function load(quiet = false) {
  if (!clusterName.value) return
  if (!quiet) loading.value = true
  try {
    const r = await veleroApi.overview()
    installed.value = r.installed
    partial.value = r.partial || []
    locations.value = r.locations ?? null
    veleroNs.value = r.namespace || ''
    backups.value = r.backups || []
    schedules.value = r.schedules || []
    restores.value = r.restores || []
  } catch {
    // RBAC/网络失败由 http 层 toast；不清空旧数据（与"请求失败≠未安装"的口径一致）
  } finally {
    loading.value = false
    syncPoll()
  }
}

function onClusterChange(name: string) {
  clusterStore.select(name)
  load()
}

onMounted(async () => {
  try {
    clusters.value = (await clusterApi.list()).map((c) => c.name)
    clusterName.value = clusterStore.current || clusters.value[0] || ''
    await load()
  } catch { /* ignore */ }
})
watch(() => clusterStore.current, (v) => {
  if (v && v !== clusterName.value) { clusterName.value = v; void load() }
})
onBeforeUnmount(() => { if (timer) clearInterval(timer) })

// ---- 展示辅助 ----
const pct = (done: number, total: number) => total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0
function progressStatus(phase?: string) {
  if (phase === 'Failed') return 'exception'
  if (phase === 'PartiallyFailed') return 'warning'
  if (phase === 'Completed') return 'success'
  return ''
}
function fmtTime(s?: string) { return s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '—' }
function fmtDurationSec(sec: number) {
  if (!sec || sec < 0) return '—'
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m}m${sec % 60 ? `${sec % 60}s` : ''}`
  return `${Math.floor(m / 60)}h${m % 60}m`
}
function nsSummary(inc?: string[], exc?: string[]) {
  const i = inc && inc.length ? `包含 ${inc.join(', ')}` : '全部命名空间'
  const e = exc && exc.length ? `，排除 ${exc.join(', ')}` : ''
  return i + e
}

const PhaseTag = {
  props: { phase: { type: String, default: '' } },
  setup(p: any) {
    const map: Record<string, [string, string]> = {
      New: ['info', '排队中'], InProgress: ['primary', '进行中'],
      Completed: ['success', '成功'], PartiallyFailed: ['warning', '部分失败'],
      Failed: ['danger', '失败'], Archived: ['info', '已归档'],
    }
    const [type, label] = map[p.phase] ?? ['info', p.phase || '未知']
    return () => h('el-tag', { size: 'small', type }, () => label)
  },
}

// ---- 详情 ----
const detailVisible = ref(false)
const detailKind = ref<'Backup' | 'Restore'>('Backup')
const detailItem = ref<VeleroBackup | VeleroRestore | null>(null)
const events = ref<VeleroEvent[]>([])
const eventsLoading = ref(false)
const volumes = ref<VeleroVolumeBackup[]>([])
const volumesLoading = ref(false)
const volumeFailures = computed(() => volumes.value.filter((v) => v.phase && v.phase !== 'Completed'))
const detailWarnings = computed(() => {
  const it = detailItem.value as any
  if (!it) return []
  return [...(it.validationWarnings || []), ...(it.validationErrors || [])]
})

async function openDetail(kind: 'Backup' | 'Restore', row: VeleroBackup | VeleroRestore) {
  detailKind.value = kind
  detailItem.value = row
  events.value = []
  volumes.value = []
  detailVisible.value = true
  eventsLoading.value = true
  try {
    events.value = await veleroApi.events(row.namespace, kind, row.name)
  } catch { /* 无事件读权限时留空，表格上已有说明 */ } finally { eventsLoading.value = false }
  if (kind === 'Backup') {
    volumesLoading.value = true
    try {
      volumes.value = await veleroApi.volumes(row.name)
    } catch { /* 无 PVB 读权限时留空 */ } finally { volumesLoading.value = false }
  }
}

// ---- 恢复 ----
const restoreDlg = ref(false)
const restoring = ref(false)
const restoreTarget = ref<VeleroBackup | null>(null)
const restoreForm = reactive({ name: '', includedNamespaces: [] as string[], restorePVs: true, preserveNodePorts: true })

function openRestore(b: VeleroBackup) {
  restoreTarget.value = b
  restoreForm.name = ''
  restoreForm.includedNamespaces = []
  restoreForm.restorePVs = true
  restoreForm.preserveNodePorts = true
  restoreDlg.value = true
}

async function doRestore() {
  const b = restoreTarget.value
  if (!b) return
  restoring.value = true
  try {
    const r = await veleroApi.createRestore({
      namespace: b.namespace, backupName: b.name,
      name: restoreForm.name.trim() || undefined,
      includedNamespaces: restoreForm.includedNamespaces.length ? restoreForm.includedNamespaces : undefined,
      restorePVs: restoreForm.restorePVs, preserveNodePorts: restoreForm.preserveNodePorts,
    })
    ElMessage.success(`恢复已发起：${r.name}，可在「恢复任务」跟踪进度`)
    restoreDlg.value = false
    await load()
  } catch { /* 名称冲突/RBAC 等已 toast */ } finally { restoring.value = false }
}

// ---- 新建备份 / 新建计划（模板字段共用一份，两弹窗互斥打开） ----
const creating = ref(false)
const backupDlg = ref(false)
const scheduleDlg = ref(false)
const backupForm = reactive({ name: '' })
const scheduleForm = reactive({ name: '', cron: '', paused: false })
const tpl = reactive({ includedNamespaces: [] as string[], excludedNamespaces: [] as string[], snapshotVolumes: true, fsBackup: false, storageLocation: '', ttlHours: 0 })
const nsOptions = ref<string[]>([])

async function ensureNsOptions() {
  if (nsOptions.value.length) return
  try {
    nsOptions.value = (await k8sApi.namespaces()).map((n) => n.name)
  } catch { nsOptions.value = [] }
}

function resetTpl() {
  tpl.includedNamespaces = []
  tpl.excludedNamespaces = []
  tpl.snapshotVolumes = true
  tpl.storageLocation = ''
  tpl.ttlHours = 0
}

function openBackupDlg() {
  backupForm.name = ''
  resetTpl()
  backupDlg.value = true
  void ensureNsOptions()
}

function openScheduleDlg() {
  editingSchedule.value = null
  scheduleForm.name = ''
  scheduleForm.cron = ''
  scheduleForm.paused = false
  resetTpl()
  scheduleDlg.value = true
  void ensureNsOptions()
}

// 编辑中的计划名（null = 新建）；保存时整体替换 spec（cron/paused/template）
const editingSchedule = ref<VeleroSchedule | null>(null)

function openEditScheduleDlg(row: VeleroSchedule) {
  editingSchedule.value = row
  scheduleForm.name = row.name
  scheduleForm.cron = row.cron
  scheduleForm.paused = row.paused
  tpl.includedNamespaces = [...(row.includedNamespaces || [])]
  tpl.excludedNamespaces = [...(row.excludedNamespaces || [])]
  tpl.snapshotVolumes = row.snapshotVolumes ?? false
  tpl.fsBackup = row.fsBackup ?? false
  tpl.storageLocation = row.storageLocation || ''
  // ttl 形如 168h0m0s → 小时数；解析失败按默认 0（= Velero 720h）
  const m = /^(\d+)h/.exec(row.ttl || '')
  tpl.ttlHours = m ? Number(m[1]) : 0
  scheduleDlg.value = true
  void ensureNsOptions()
}

function tplBody() {
  return {
    namespace: veleroNs.value || undefined,
    includedNamespaces: tpl.includedNamespaces.length ? tpl.includedNamespaces : undefined,
    excludedNamespaces: tpl.excludedNamespaces.length ? tpl.excludedNamespaces : undefined,
    snapshotVolumes: tpl.snapshotVolumes,
    fsBackup: tpl.fsBackup,
    storageLocation: tpl.storageLocation || undefined,
    ttlHours: tpl.ttlHours || undefined,
  }
}

async function doCreateBackup() {
  creating.value = true
  try {
    const r = await veleroApi.createBackup({ ...tplBody(), name: backupForm.name.trim() || undefined })
    ElMessage.success(`备份已发起：${r.name}`)
    backupDlg.value = false
    await load()
  } catch { /* 名称冲突/RBAC 等已 toast */ } finally { creating.value = false }
}

async function doCreateSchedule() {
  if (!scheduleForm.name.trim() || !scheduleForm.cron.trim()) {
    ElMessage.warning('名称与 Cron 必填')
    return
  }
  creating.value = true
  try {
    const body = { ...tplBody(), cron: scheduleForm.cron.trim(), paused: scheduleForm.paused }
    if (editingSchedule.value) {
      await veleroApi.updateSchedule(editingSchedule.value.namespace, editingSchedule.value.name, body)
      ElMessage.success('计划已更新，后续备份按新模板执行')
    } else {
      const r = await veleroApi.createSchedule({ ...body, name: scheduleForm.name.trim() })
      ElMessage.success(`计划已创建：${r.name}`)
    }
    scheduleDlg.value = false
    await load()
  } catch { /* cron 非法等已 toast 明确原因 */ } finally { creating.value = false }
}

// ---- 计划启停 / 删除 ----
async function setPaused(row: VeleroSchedule, enable: boolean) {
  try {
    await veleroApi.pauseSchedule(row.namespace, row.name, !enable)
    ElMessage.success(enable ? '计划已启用' : '计划已暂停')
    await load()
  } catch { /* 失败已 toast，开关按数据源回弹 */ await load() }
}

async function delSchedule(row: VeleroSchedule) {
  try {
    await confirmDelete(row.name, { warning: '仅删除计划本身，历史备份不受影响；删除后不再产生新的定时备份' })
  } catch { return }
  try {
    await veleroApi.deleteSchedule(row.namespace, row.name)
    ElMessage.success('计划已删除')
    await load()
  } catch { /* 已 toast */ }
}

async function delBackup(row: VeleroBackup) {
  try {
    await confirmDelete(row.name, { warning: '删除备份记录会连带删除对象存储中的备份数据，不可恢复' })
  } catch { return }
  try {
    await veleroApi.deleteBackup(row.namespace, row.name)
    ElMessage.success('已删除')
    await load()
  } catch { /* 进行中拒删等已 toast */ }
}

async function delRestore(row: VeleroRestore) {
  try {
    await confirmDelete(row.name, { warning: '仅删除恢复任务记录，已恢复出来的资源不受影响' })
  } catch { return }
  try {
    await veleroApi.deleteRestore(row.namespace, row.name)
    ElMessage.success('已删除')
    await load()
  } catch { /* 已 toast */ }
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.mono { font-family: var(--kc-mono); font-size: 13px; }
.fail-hint { color: var(--el-color-danger); font-weight: 400; font-size: 13px; }
.hd-actions { display: flex; align-items: center; }
.poll-tag { margin-right: 8px; }
.stat-row { display: flex; gap: 12px; margin-bottom: 8px; flex-wrap: wrap; }
.stat-card { flex: 1; min-width: 100px; background: var(--el-fill-color-light); border-radius: 6px; padding: 10px 14px; }
.stat-num { font-size: 20px; font-weight: 600; }
.stat-card.ok .stat-num { color: var(--el-color-success); }
.stat-card.bad .stat-num { color: var(--el-color-danger); }
.stat-card.warn .stat-num { color: var(--el-color-warning); }
.stat-label { color: var(--el-text-color-secondary); }
.sec-title { font-weight: 600; margin: 16px 0 8px; }
.dim { color: var(--el-text-color-secondary); }
.txt-warn { color: var(--el-color-warning); }
.mono { font-family: Consolas, 'Courier New', monospace; }
.tpl-tag { margin: 0 6px; }
.warn-list { margin: 0; padding-left: 18px; }
.warn-list li { margin: 2px 0; color: var(--el-color-danger); }
.detail :deep(.el-descriptions) { margin-bottom: 8px; }
</style>
