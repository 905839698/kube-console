<!-- CI 流水线（内置 Tekton，集群/命名空间隔离）：执行中心 / 流水线 / 项目 / 凭据。
     数据经 /api/ci/*（http 层自动带 X-Cluster，切集群自动重载）；
     实时状态 = WS 推送 + 5s 轮询兜底；任务日志 = SSE（RunLog）。 -->
<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>CI 流水线（内置 Tekton）</span>
        <div>
          <el-tag v-if="ready" :type="tektonReady ? 'success' : 'info'" size="small" style="margin-right: 8px">
            {{ tektonReady ? 'Tekton 就绪' : '当前集群未安装 Tekton' }}
          </el-tag>
          <el-button size="small" :icon="Refresh" circle @click="reloadAll" />
        </div>
      </div>
    </template>

    <el-alert v-if="ready && !tektonReady" type="warning" :closable="false" style="margin-bottom: 12px"
      title="当前集群未安装 Tekton Pipelines：流水线无法执行（浏览配置不受影响）。" />

    <el-tabs v-model="tab">
      <!-- ============ 执行中心 ============ -->
      <el-tab-pane label="执行中心" name="runs">
        <div class="stat-row">
          <div class="stat-card"><div class="stat-num">{{ stats.total }}</div><div class="stat-label">总运行（当前页）</div></div>
          <div class="stat-card ok"><div class="stat-num">{{ stats.success }}</div><div class="stat-label">成功</div></div>
          <div class="stat-card bad"><div class="stat-num">{{ stats.failed }}</div><div class="stat-label">失败</div></div>
          <div class="stat-card"><div class="stat-num">{{ stats.running }}</div><div class="stat-label">进行中</div></div>
          <div class="stat-card"><div class="stat-num">{{ stats.rate }}%</div><div class="stat-label">成功率</div></div>
        </div>
        <div class="toolbar">
          <el-select v-model="filterPipeline" clearable placeholder="按流水线筛选" style="width: 240px" @change="loadRuns(1)">
            <el-option v-for="p in pipelines" :key="p.id" :label="p.name" :value="p.id" />
          </el-select>
        </div>
        <el-table border :data="runs" v-loading="runsLoading" size="small" stripe highlight-current-row @row-click="(r: any) => openDetail(r)" row-class-name="cursor-row">
          <el-table-column label="#" width="90">
            <template #default="{ row }">
              <span :class="['dot', 'st-' + row.status]"></span>#{{ row.runNo }}
            </template>
          </el-table-column>
          <el-table-column label="流水线" min-width="180" show-overflow-tooltip>
            <template #default="{ row }">{{ pipelineName(row.pipelineId) }}</template>
          </el-table-column>
          <el-table-column label="状态" width="100" align="center">
            <template #default="{ row }"><RunStatusTag :status="row.status" /></template>
          </el-table-column>
          <el-table-column prop="gitBranch" label="分支" width="110" />
          <el-table-column label="提交" width="100">
            <template #default="{ row }"><span class="mono">{{ (row.gitCommit || '').slice(0, 7) }}</span></template>
          </el-table-column>
          <el-table-column prop="startedBy" label="触发" width="90" />
          <el-table-column label="开始时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.startedAt) }}</template>
          </el-table-column>
          <el-table-column label="耗时" width="90" align="center">
            <template #default="{ row }">{{ fmtDuration(row.startedAt, row.finishedAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="150" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" @click.stop="openDetail(row)">详情</el-button>
              <el-button size="small" text type="warning" :disabled="!canWrite" @click.stop="rerun(row)">重跑</el-button>
              <el-button size="small" text type="danger" :disabled="!canWrite || isFinished(row.status)" @click.stop="stopRun(row)">停止</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-pagination
          v-model:current-page="runPage" :page-size="20" :total="runTotal"
          layout="total, prev, pager, next" background
          style="margin-top: 12px; justify-content: flex-end"
          @current-change="loadRuns(runPage)"
        />
      </el-tab-pane>

      <!-- ============ 流水线 ============ -->
      <el-tab-pane label="流水线" name="pipelines">
        <div class="toolbar">
          <el-select v-model="filterProject" clearable placeholder="按项目筛选" style="width: 220px" @change="loadPipelines">
            <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
          </el-select>
          <el-button v-if="canWrite" type="primary" size="small" :disabled="!projects.length" @click="openPipeDlg()">新建流水线</el-button>
        </div>
        <el-table border :data="pipelines" v-loading="loading" size="small" stripe>
          <el-table-column prop="name" label="流水线" min-width="200" />
          <el-table-column prop="description" label="描述" min-width="160" show-overflow-tooltip />
          <el-table-column label="所属项目" min-width="130">
            <template #default="{ row }">{{ projectName(row.projectId) }}</template>
          </el-table-column>
          <el-table-column label="版本" width="80" align="center">
            <template #default="{ row }"><el-tag v-if="row.latestVersion" size="small" type="primary">v{{ row.latestVersion }}</el-tag><span v-else class="dim">—</span></template>
          </el-table-column>
          <el-table-column label="操作" width="300" fixed="right">
            <template #default="{ row }">
              <el-button size="small" type="primary" plain @click="openDesigner(row)">设计器</el-button>
              <el-button v-if="row.latestVersion" size="small" text @click="openRun(row)">运行</el-button>
              <el-button size="small" text @click="openPipeDlg(row)">编辑</el-button>
              <el-button size="small" text type="warning" :disabled="!canWrite" @click="dupPipe(row)">复制</el-button>
              <el-button size="small" text type="danger" :disabled="!canWrite" @click="delPipe(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div v-if="!projects.length" class="empty-tip">当前集群还没有 CI 项目——先到「项目」tab 创建（项目 = 一个 K8s 命名空间）。</div>
      </el-tab-pane>

      <!-- ============ 项目（= 命名空间隔离单元） ============ -->
      <el-tab-pane label="项目" name="projects">
        <div class="toolbar">
          <el-button v-if="canWrite" type="primary" size="small" @click="openProjDlg()">新建项目</el-button>
          <span class="cfg-tip">项目 = 一个 K8s 命名空间：该项目的流水线运行 / 工作区 PVC / 凭证 Secret 都落在项目 ns 内（自动创建）。</span>
        </div>
        <el-table border :data="projects" v-loading="loading" size="small" stripe>
          <el-table-column label="项目" min-width="150">
            <template #default="{ row }">{{ row.displayName || row.name }}</template>
          </el-table-column>
          <el-table-column prop="name" label="标识" min-width="130" />
          <el-table-column prop="namespace" label="命名空间" min-width="140">
            <template #default="{ row }"><span class="mono">{{ row.namespace }}</span></template>
          </el-table-column>
          <el-table-column prop="description" label="描述" min-width="180" show-overflow-tooltip />
          <el-table-column label="创建时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="150" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text :disabled="!canWrite" @click="openProjDlg(row)">编辑</el-button>
              <el-button size="small" text type="danger" :disabled="!canWrite" @click="delProj(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- ============ 凭据 ============ -->
      <el-tab-pane label="凭据" name="credentials" lazy>
        <div class="toolbar">
          <el-button v-if="canWrite" type="primary" size="small" @click="openCredDlg()">新建凭据</el-button>
          <span class="cfg-tip">凭据供流水线节点引用；明文只存 K8s Secret（平台 ns + 各项目 ns 扇出）。</span>
        </div>
        <el-table border :data="credentials" v-loading="credLoading" size="small" stripe>
          <el-table-column prop="name" label="名称" min-width="170" />
          <el-table-column label="形态" width="150" align="center">
            <template #default="{ row }"><el-tag size="small" type="info">{{ credFormLabel(row.form) }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="secretName" label="K8s Secret" min-width="150">
            <template #default="{ row }"><span class="mono">{{ row.secretName }}</span></template>
          </el-table-column>
          <el-table-column label="范围" width="100" align="center">
            <template #default="{ row }">{{ row.projectId ? ('项目 ' + row.projectId) : '平台级' }}</template>
          </el-table-column>
          <el-table-column label="被引用" min-width="150">
            <template #default="{ row }">
              <el-tag v-for="r in row.references ?? []" :key="r" size="small" style="margin-right: 4px">{{ r }}</el-tag>
              <span v-if="!(row.references?.length)" class="dim">—</span>
            </template>
          </el-table-column>
          <el-table-column label="更新时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.updatedAt || row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="130" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" :disabled="!canWrite" @click="openCredDlg(row)">编辑</el-button>
              <el-tooltip :disabled="!(row.references?.length)" :content="`被 ${row.references?.length} 条流水线引用，请先解除引用`">
                <el-button size="small" text type="danger" :disabled="!canWrite || (row.references?.length > 0)" @click="delCred(row)">删除</el-button>
              </el-tooltip>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
      <!-- ============ 制品 ============ -->
      <el-tab-pane label="制品" name="artifacts" lazy>
        <div class="toolbar">
          <el-select v-model="filterArtProject" clearable placeholder="按项目筛选" style="width: 220px" @change="loadArtifacts(1)">
            <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
          </el-select>
        </div>
        <el-table border :data="artifacts" v-loading="artLoading" size="small" stripe @row-click="(r: any) => openArtifact(r)" row-class-name="cursor-row">
          <el-table-column prop="name" label="制品" min-width="200" show-overflow-tooltip />
          <el-table-column prop="type" label="类型" width="100" align="center">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.type }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="version" label="版本" width="110">
            <template #default="{ row }">{{ row.version || '—' }}</template>
          </el-table-column>
          <el-table-column prop="storageType" label="存储" width="90" align="center">
            <template #default="{ row }"><el-tag size="small">{{ row.storageType }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="storagePath" label="路径/引用" min-width="220" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.storagePath }}</span></template>
          </el-table-column>
          <el-table-column label="构建时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button v-if="row.storageType !== 'harbor'" size="small" text type="primary" @click.stop="downloadArtifact(row)">下载</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- ============ 发布记录 ============ -->
      <el-tab-pane label="发布记录" name="deployments" lazy>
        <div class="toolbar">
          <el-select v-model="filterDeployProject" clearable placeholder="按项目筛选" style="width: 220px" @change="loadDeployments(1)">
            <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
          </el-select>
        </div>
        <el-table border :data="deployments" v-loading="deployLoading" size="small" stripe>
          <el-table-column prop="namespace" label="命名空间" min-width="130" />
          <el-table-column label="类型" width="120" align="center">
            <template #default="{ row }">
              <el-tag size="small" :type="row.kind === 'rollback' ? 'warning' : 'primary'">{{ row.kind }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="目标 workload" min-width="200" show-overflow-tooltip>
            <template #default="{ row }">{{ (row.targets || []).map((t: any) => t.name).join(', ') || '—' }}</template>
          </el-table-column>
          <el-table-column label="状态" width="90" align="center">
            <template #default="{ row }"><el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">{{ row.status === 'success' ? '成功' : '失败' }}</el-tag></template>
          </el-table-column>
          <el-table-column label="分支" width="110">
            <template #default="{ row }">{{ row.gitBranch || '—' }}</template>
          </el-table-column>
          <el-table-column label="提交" width="90">
            <template #default="{ row }"><span class="mono">{{ (row.gitCommit || '').slice(0, 7) || '—' }}</span></template>
          </el-table-column>
          <el-table-column prop="createdBy" label="操作人" width="100" />
          <el-table-column label="时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="100" fixed="right">
            <template #default="{ row }">
              <el-tooltip :disabled="row.kind === 'rollback' || !canWrite" content="回滚记录不能再回滚">
                <el-button size="small" text type="warning" :disabled="row.kind === 'rollback' || !canWrite" @click="doRollback(row)">回滚</el-button>
              </el-tooltip>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- ============ 全局变量（${global.KEY} 引用） ============ -->
      <el-tab-pane label="全局变量" name="globals" lazy>
        <div class="toolbar">
          <el-button v-if="canWrite" type="primary" size="small" @click="openGlobalDlg()">新建变量</el-button>
          <span class="cfg-tip">节点参数中以 <code>${'{'}global.KEY{'}'}</code> 引用（如 <code>${'{'}global.HARBOR{'}'}/app</code>）；编译前替换为变量值，未定义的 key 原样保留。</span>
        </div>
        <el-table border :data="globals" v-loading="globalLoading" size="small" stripe>
          <el-table-column prop="key" label="Key" min-width="180">
            <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
          </el-table-column>
          <el-table-column prop="value" label="值" min-width="260" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.value || '—' }}</span></template>
          </el-table-column>
          <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
          <el-table-column label="更新时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.updatedAt || row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="130" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" :disabled="!canWrite" @click="openGlobalDlg(row)">编辑</el-button>
              <el-button size="small" text type="danger" :disabled="!canWrite" @click="delGlobal(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <!-- 制品详情（血缘） -->
    <el-dialog v-model="artifactDlg" title="制品详情" width="640px">
      <el-descriptions :column="2" size="small" border v-if="artifactDetail">
        <el-descriptions-item label="名称">{{ artifactDetail.name }}</el-descriptions-item>
        <el-descriptions-item label="类型">{{ artifactDetail.type }}</el-descriptions-item>
        <el-descriptions-item label="版本">{{ artifactDetail.version || '—' }}</el-descriptions-item>
        <el-descriptions-item label="存储">{{ artifactDetail.storageType }} · <span class="mono">{{ artifactDetail.storagePath }}</span></el-descriptions-item>
        <el-descriptions-item label="流水线">{{ artifactDetail.pipelineName || '—' }} #{{ artifactDetail.runNo }}</el-descriptions-item>
        <el-descriptions-item label="触发">{{ artifactDetail.startedBy || '—' }} · {{ artifactDetail.runStartedAt || '—' }}</el-descriptions-item>
        <el-descriptions-item label="分支">{{ artifactDetail.gitBranch || '—' }}</el-descriptions-item>
        <el-descriptions-item label="提交"><span class="mono">{{ (artifactDetail.gitCommit || '').slice(0, 10) || '—' }}</span></el-descriptions-item>
        <el-descriptions-item v-if="artifactDetail.digest" label="Digest" :span="2"><span class="mono">{{ artifactDetail.digest }}</span></el-descriptions-item>
        <el-descriptions-item v-if="artifactDetail.pullCommand" label="拉取命令" :span="2"><span class="mono">{{ artifactDetail.pullCommand }}</span></el-descriptions-item>
      </el-descriptions>
      <template #footer>
        <el-button v-if="artifactDetail && artifactDetail.storageType !== 'harbor'" type="primary" @click="downloadArtifact(artifactDetail)">下载</el-button>
        <el-button @click="artifactDlg = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 全局变量新建/编辑 -->
    <el-dialog v-model="globalDlg" :title="globalForm.id ? '编辑全局变量' : '新建全局变量'" width="480px">
      <el-form label-width="90px" size="small">
        <el-form-item label="Key" required>
          <el-input v-model="globalForm.key" :disabled="!!globalForm.id" placeholder="大写字母/数字/下划线，如 HARBOR" />
        </el-form-item>
        <el-form-item label="值">
          <el-input v-model="globalForm.value" placeholder="如 harbor.cqyxpt.site:8443/release" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="globalForm.description" placeholder="用途说明（可选）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="globalDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveGlobal">保存</el-button>
      </template>
    </el-dialog>

    <!-- 运行详情抽屉 -->
    <el-drawer v-model="detailVisible" :title="`运行详情 #${detail?.run.runNo ?? ''} · ${pipelineName(detail?.run.pipelineId ?? 0)}`" size="62%" @closed="onDetailClosed">
      <template v-if="detail">
        <el-descriptions :column="3" size="small" border style="margin-bottom: 14px">
          <el-descriptions-item label="状态"><RunStatusTag :status="detail.run.status" /></el-descriptions-item>
          <el-descriptions-item label="分支">{{ detail.run.gitBranch || '—' }}</el-descriptions-item>
          <el-descriptions-item label="提交"><span class="mono">{{ (detail.run.gitCommit || '').slice(0, 10) || '—' }}</span></el-descriptions-item>
          <el-descriptions-item label="触发">{{ detail.run.triggerType }} / {{ detail.run.startedBy }}</el-descriptions-item>
          <el-descriptions-item label="开始">{{ fmtTime(detail.run.startedAt) }}</el-descriptions-item>
          <el-descriptions-item label="结束">{{ fmtTime(detail.run.finishedAt) }}</el-descriptions-item>
        </el-descriptions>
        <div class="sec-title">
          任务
          <el-tag v-if="!isFinished(detail.run.status)" size="small" type="primary" style="margin-left: 8px">实时刷新中</el-tag>
          <el-button size="small" text type="primary" style="float: right" :disabled="!detail.run.versionId" @click="openDag">DAG 视图</el-button>
        </div>
        <el-empty v-if="!detail.tasks.length" description="暂无任务" :image-size="60" />
        <el-timeline v-else>
          <el-timeline-item
            v-for="t in detail.tasks" :key="t.id"
            :type="t.status === 'success' ? 'success' : t.status === 'failed' ? 'danger' : t.status === 'running' ? 'primary' : 'info'"
          >
            <div class="task-row">
              <span class="task-name">{{ t.name }}</span>
              <span class="task-type">{{ t.nodeType }}</span>
              <span class="task-dur">{{ fmtDuration(t.startedAt, t.finishedAt) }}</span>
              <template v-if="t.nodeType === 'approval'">
                <template v-if="t.status === 'pending' || t.status === 'running'">
                  <el-button size="small" type="primary" :loading="approving === t.id" @click="decide(t, 'approved')">通过</el-button>
                  <el-button size="small" type="danger" :loading="approving === t.id" @click="decide(t, 'rejected')">驳回</el-button>
                </template>
                <el-tag v-else size="small" :type="t.status === 'success' ? 'success' : 'danger'">{{ t.status === 'success' ? '已通过' : '已结束' }}</el-tag>
              </template>
              <el-button size="small" text type="primary" @click="openLogs(t)">日志</el-button>
            </div>
            <el-descriptions v-if="t.results && Object.keys(t.results).length" :column="1" size="small" border class="task-results">
              <el-descriptions-item v-for="(v, k) in t.results" :key="k" :label="String(k)">{{ v }}</el-descriptions-item>
            </el-descriptions>
          </el-timeline-item>
        </el-timeline>
      </template>
    </el-drawer>

    <!-- 日志抽屉 -->
    <el-drawer v-model="logVisible" :title="`日志 — ${logTask?.name ?? ''}（${logTask?.nodeType ?? ''}）`" size="55%" destroy-on-close>
      <RunLog v-if="logTask && detail" :run-id="detail.run.id" :task-id="logTask.id" :running="!isFinished(logTask.status)" />
    </el-drawer>

    <!-- DAG 视图 -->
    <el-dialog v-model="dagVisible" :title="`执行 DAG — ${pipelineName(detail?.run.pipelineId ?? 0)} #${detail?.run.runNo ?? ''}`" width="920px">
      <div class="dag-box" v-loading="dagLoading">
        <VueFlow v-if="dagFlow.nodes.length" :nodes="dagFlow.nodes" :edges="dagFlow.edges" fit-view-on-init
          :nodes-draggable="false" :nodes-connectable="false" :elements-selectable="false">
          <template #node-dag="{ data }">
            <div class="dag-node" :style="data.style">{{ data.label }}</div>
          </template>
          <Background />
          <Controls :show-interactive="false" />
        </VueFlow>
        <div v-else-if="!dagLoading" class="dim" style="text-align: center; padding: 60px 0">该版本没有可用的 DSL 图</div>
      </div>
      <div class="dag-legend">
        节点颜色：
        <el-tag type="success" size="small">成功</el-tag><el-tag type="primary" size="small">运行中</el-tag>
        <el-tag type="danger" size="small">失败</el-tag><el-tag type="warning" size="small">已取消</el-tag>
        <el-tag size="small">等待中</el-tag>
      </div>
    </el-dialog>

    <!-- 运行对话框 -->
    <el-dialog v-model="runDlg" :title="`运行流水线：${runTarget?.name ?? ''}`" width="460px">
      <div class="cfg-tip" style="margin-bottom: 10px">选择分支或 tag（留空按流水线默认 {{ runRefs.def || 'main' }} 运行；可手输）</div>
      <el-radio-group v-model="runMode" style="margin-bottom: 10px" @change="runRef = ''">
        <el-radio-button value="branch">分支</el-radio-button>
        <el-radio-button value="tag">Tag</el-radio-button>
      </el-radio-group>
      <el-select
        v-model="runRef" filterable allow-create default-first-option clearable
        :placeholder="runMode === 'branch' ? '如 feature/xxx（留空=默认）' : '如 v1.2.0（留空=默认）'"
        style="width: 100%"
      >
        <el-option v-for="r in (runMode === 'branch' ? runRefs.branches : runRefs.tags)" :key="r" :label="r" :value="r" />
      </el-select>
      <div v-if="runRefs.err" style="margin-top: 8px; color: #e6a23c; font-size: 12px">{{ runRefs.err }}</div>
      <template #footer>
        <el-button @click="runDlg = false">取消</el-button>
        <el-button type="primary" :loading="running" @click="doRun">运行</el-button>
      </template>
    </el-dialog>

    <!-- 流水线新建/编辑 -->
    <el-dialog v-model="pipeDlg" :title="pipeForm.id ? '编辑流水线' : '新建流水线'" width="440px">
      <el-form label-width="90px" size="small">
        <el-form-item label="所属项目" required>
          <el-select v-model="pipeForm.projectId" :disabled="!!pipeForm.id" style="width: 100%">
            <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="名称" required><el-input v-model="pipeForm.name" placeholder="小写字母/数字/中划线" :disabled="!!pipeForm.id" /></el-form-item>
        <el-form-item label="描述"><el-input v-model="pipeForm.description" /></el-form-item>
      </el-form>
      <div v-if="!pipeForm.id" class="cfg-tip">创建后点「设计器」编排节点。</div>
      <template #footer>
        <el-button @click="pipeDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="savePipe">保存</el-button>
      </template>
    </el-dialog>

    <!-- 项目新建/编辑 -->
    <el-dialog v-model="projDlg" :title="projForm.id ? '编辑项目' : '新建项目'" width="460px">
      <el-form label-width="90px" size="small">
        <el-form-item label="标识" required><el-input v-model="projForm.name" :disabled="!!projForm.id" placeholder="小写字母/数字/中划线，如 demo-service" /></el-form-item>
        <el-form-item label="显示名"><el-input v-model="projForm.displayName" placeholder="如 演示服务" /></el-form-item>
        <el-form-item label="命名空间" required>
          <el-input v-model="projForm.namespace" :disabled="!!projForm.id" placeholder="K8s 命名空间，如 ci-projects（不存在会自动创建）" />
          <div class="cfg-tip" v-if="!projForm.id">命名空间创建后不可更改（项目内资源都落在该 ns）。</div>
        </el-form-item>
        <el-form-item label="描述"><el-input v-model="projForm.description" type="textarea" :rows="2" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="projDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveProj">保存</el-button>
      </template>
    </el-dialog>

    <!-- 凭据新建/编辑 -->
    <el-dialog v-model="credDlg" :title="credForm.id ? '编辑凭据（留空字段保持原值）' : '新建凭据'" width="560px">
      <el-form label-width="100px" size="small">
        <el-form-item label="名称" required><el-input v-model="credForm.name" :disabled="!!credForm.id" placeholder="如 git-push-token" /></el-form-item>
        <el-form-item label="形态" v-if="!credForm.id">
          <el-select v-model="credForm.form" style="width: 100%">
            <el-option v-for="f in credForms" :key="f.value" :label="f.label" :value="f.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="用途标签">
          <el-select v-model="credForm.type" clearable style="width: 100%" placeholder="可选">
            <el-option v-for="t in ['git', 'registry', 'k8s', 'cloud', 'generic']" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item>
        <el-form-item label="Host" v-if="['basic', 'token', 'dockerconfig'].includes(credForm.form)">
          <el-input v-model="credForm.host" placeholder="如 harbor.cqyxpt.site / gitlab.cqyxpt.site（可选）" />
        </el-form-item>
        <el-form-item label="用户名" v-if="['basic', 'dockerconfig'].includes(credForm.form)">
          <el-input v-model="credForm.username" />
        </el-form-item>
        <el-form-item label="密码" v-if="['basic', 'dockerconfig'].includes(credForm.form)">
          <el-input v-model="credForm.password" type="password" show-password placeholder="留空保持原值" />
        </el-form-item>
        <el-form-item label="Token" v-if="credForm.form === 'token'">
          <el-input v-model="credForm.token" type="password" show-password placeholder="glpat-... / ghp_..." />
        </el-form-item>
        <el-form-item v-if="credForm.form === 'dockerconfig'">
          <template #label><span>整包 JSON（可选）</span></template>
          <el-input v-model="credForm.dockerconfig" type="textarea" :rows="4" placeholder='粘贴 ~/.docker/config.json 整包；留空则按用户名/密码自动拼装' />
        </el-form-item>
        <el-form-item label="kubeconfig" v-if="credForm.form === 'kubeconfig'">
          <el-input v-model="credForm.kubeconfig" type="textarea" :rows="8" placeholder="粘贴 kubeconfig 全文（留空保持原值）" />
        </el-form-item>
        <el-form-item label="AccessKey" v-if="credForm.form === 'aksk'"><el-input v-model="credForm.accessKey" /></el-form-item>
        <el-form-item label="SecretKey" v-if="credForm.form === 'aksk'">
          <el-input v-model="credForm.secretKey" type="password" show-password placeholder="留空保持原值" />
        </el-form-item>
        <el-form-item label="Region" v-if="credForm.form === 'aksk'"><el-input v-model="credForm.region" placeholder="可选" /></el-form-item>
        <el-form-item label="Endpoint" v-if="credForm.form === 'aksk'"><el-input v-model="credForm.endpoint" placeholder="可选" /></el-form-item>
        <el-form-item label="键值对" v-if="credForm.form === 'raw'">
          <div style="width: 100%">
            <div v-for="(kv, ki) in credForm.data" :key="ki" class="cred-kv">
              <el-input v-model="kv.k" placeholder="key" style="width: 38%" size="small" />
              <el-input v-model="kv.v" placeholder="value" style="width: 50%" size="small" show-password />
              <el-button size="small" type="danger" text @click="credForm.data.splice(ki, 1)">删</el-button>
            </div>
            <el-button size="small" plain @click="credForm.data.push({ k: '', v: '' })">添加键值</el-button>
          </div>
        </el-form-item>
        <el-form-item label="所属项目">
          <el-select v-model="credForm.projectId" clearable style="width: 100%" placeholder="平台级（全部项目可用）">
            <el-option v-for="p in projects" :key="p.id" :label="p.displayName || p.name" :value="p.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="credDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveCred">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { VueFlow, MarkerType } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import {
  ciApi, runWsUrl,
  type CIGraph, type CIPEdge, type CIProject, type CIPipeline, type CIRun, type CIRunDetail, type CITaskRun, type CICIRReady, type CIGlobalVar, type CIArtifact, type CIArtifactDetail, type CIDeployment,
} from '../api/ci'
import { useUserStore } from '../store/user'
import { activeCluster } from '../store/clusterRef'
import { invalidateNodeTypes } from './ci/nodes'
import RunLog from './ci/RunLog.vue'
import { confirmDelete } from '../utils/confirm'

const userStore = useUserStore()
const route = useRoute()
const router = useRouter()
const canWrite = computed(() => userStore.isAdmin)

// ---- 状态 ----
const ready = ref(false)
const tektonReady = ref(false)
const tab = ref('runs')
const projects = ref<CIProject[]>([])
const pipelines = ref<CIPipeline[]>([])
const filterProject = ref<number | undefined>()
const filterPipeline = ref<number | undefined>()
const loading = ref(false)
const runs = ref<CIRun[]>([])
const runsLoading = ref(false)
const runTotal = ref(0)
const runPage = ref(1)
const credentials = ref<any[]>([])
const credLoading = ref(false)
const saving = ref(false)

// ---- 详情 / 日志 / DAG ----
const detailVisible = ref(false)
const detail = ref<CIRunDetail | null>(null)
const logVisible = ref(false)
const logTask = ref<CITaskRun | null>(null)
const approving = ref<number | null>(null)
const dagVisible = ref(false)
const dagLoading = ref(false)
const dagGraph = ref<CIGraph | null>(null)
let ws: WebSocket | null = null
let detailTimer: ReturnType<typeof setInterval> | null = null
let listTimer: ReturnType<typeof setInterval> | null = null
let detailSeq = 0
let wsAlive = false

// ---- 运行 ----
const runDlg = ref(false)
const running = ref(false)
const runTarget = ref<CIPipeline>()
const runMode = ref<'branch' | 'tag'>('branch')
const runRef = ref('')
const runRefs = reactive<{ branches: string[]; tags: string[]; def: string; err?: string }>({ branches: [], tags: [], def: 'main' })

// ---- 表单 ----
const pipeDlg = ref(false)
const pipeForm = reactive<{ id?: number; projectId?: number; name: string; description: string }>({ name: '', description: '' })
const projDlg = ref(false)
const projForm = reactive<{ id?: number; name: string; displayName: string; namespace: string; description: string }>({ name: '', displayName: '', namespace: '', description: '' })
const credDlg = ref(false)
const credForms = [
  { value: 'basic', label: '账密 (username/password)' },
  { value: 'token', label: 'Token (单令牌)' },
  { value: 'dockerconfig', label: 'Docker Config (整包/拼装)' },
  { value: 'kubeconfig', label: 'Kubeconfig (集群)' },
  { value: 'aksk', label: 'AK/SK (云厂商)' },
  { value: 'raw', label: '自定义 (raw 键值对)' },
]
const credForm = reactive<Record<string, any>>({
  id: undefined, name: '', form: 'basic', type: '', host: '', username: '',
  password: '', token: '', dockerconfig: '', kubeconfig: '', accessKey: '', secretKey: '',
  region: '', endpoint: '', data: [] as { k: string; v: string }[], projectId: undefined as number | undefined,
})

// ---- 基础函数 ----
const isFinished = (s?: string) => !!s && ['success', 'failed', 'cancelled'].includes(s)
function fmtTime(s?: string | null) { return s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '-' }
function fmtDuration(start?: string | null, end?: string | null) {
  if (!start) return '—'
  const s = new Date(start).getTime()
  const e = end ? new Date(end).getTime() : Date.now()
  const sec = Math.max(0, Math.round((e - s) / 1000))
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m}m${sec % 60 ? `${sec % 60}s` : ''}`
  return `${Math.floor(m / 60)}h${m % 60}m`
}
function credFormLabel(f: string) { return credForms.find((o) => o.value === f)?.label ?? f }
function projectName(id: number) {
  const p = projects.value.find((x) => x.id === id)
  return p ? (p.displayName || p.name) : `#${id}`
}
function pipelineName(id: number) {
  return pipelines.value.find((x) => x.id === id)?.name ?? `#${id}`
}
const stats = computed(() => {
  let success = 0, failed = 0, running = 0
  for (const r of runs.value) {
    if (r.status === 'success') success++
    else if (r.status === 'failed') failed++
    else if (!isFinished(r.status)) running++
  }
  const done = success + failed
  return { total: runs.value.length, success, failed, running, rate: done ? Math.round((success / done) * 100) : 100 }
})

// ---- 数据加载（切集群自动重载） ----
async function loadReady() {
  try {
    const r = await ciApi.ready()
    tektonReady.value = r.tektonInstalled
    ready.value = true
  } catch { ready.value = false }
}
async function loadProjects() {
  try { projects.value = await ciApi.projects() } catch { projects.value = [] }
}
async function loadPipelines() {
  loading.value = true
  try { pipelines.value = await ciApi.pipelines(filterProject.value) } catch { pipelines.value = [] } finally { loading.value = false }
}
async function loadRuns(p = 1) {
  runsLoading.value = true
  runPage.value = p
  try {
    const r = await ciApi.runs({ pipelineId: filterPipeline.value, page: p, size: 20 })
    runs.value = r.items ?? []
    runTotal.value = r.total ?? 0
  } catch { runs.value = []; runTotal.value = 0 } finally { runsLoading.value = false }
}
async function loadCredentials() {
  credLoading.value = true
  try { credentials.value = await ciApi.credentials() } catch { credentials.value = [] } finally { credLoading.value = false }
}
function reloadAll() {
  void loadReady(); void loadProjects(); void loadPipelines(); void loadRuns(runPage.value); void loadCredentials(); void loadGlobals(); void loadArtifacts(); void loadDeployments()
}

// ---- 详情 + 实时状态（WS + 5s 轮询兜底；终态停止） ----
function openDetail(r: CIRun) {
  void loadDetail(r.id)
  detailVisible.value = true
}
async function loadDetail(id: number) {
  const seq = ++detailSeq
  try {
    const d = await ciApi.runDetail(id)
    if (seq !== detailSeq) return
    detail.value = d
    detailVisible.value = true
  } catch {
    if (seq === detailSeq) detail.value = null
  }
  syncDetailLive()
}
function syncDetailLive() {
  if (detailTimer) { clearInterval(detailTimer); detailTimer = null }
  const active = detail.value && !isFinished(detail.value.run.status)
  if (active && !wsAlive) {
    detailTimer = setInterval(() => { void loadDetail(detail.value!.run.id) }, 5000)
  }
}
function connectWs(runId: number) {
  ws?.close(); ws = null; wsAlive = false
  void runWsUrl(runId).then((url) => {
    try {
      ws = new WebSocket(url)
      ws.onopen = () => { wsAlive = true; syncDetailLive() }
      ws.onmessage = (e) => {
        try {
          const msg = JSON.parse(e.data)
          if (msg.type === 'run' && detail.value) void loadDetail(detail.value.run.id)
        } catch { /* ignore */ }
      }
      ws.onclose = () => {
        wsAlive = false
        if (detail.value && !isFinished(detail.value.run.status)) {
          setTimeout(() => {
            if (detail.value && !isFinished(detail.value.run.status)) connectWs(detail.value.run.id)
          }, 5000)
        }
        syncDetailLive()
      }
    } catch { /* 轮询兜底 */ }
  }).catch(() => { /* 轮询兜底 */ })
}
watch(detail, (d) => {
  if (d) connectWs(d.run.id)
})
function onDetailClosed() {
  detail.value = null
  ws?.close(); ws = null; wsAlive = false
  if (detailTimer) { clearInterval(detailTimer); detailTimer = null }
}

// 列表有进行中时 5s 自动刷新
watch([runs, () => detail.value?.run.status], () => {
  if (listTimer) { clearInterval(listTimer); listTimer = null }
  const hasActive = runs.value.some((r) => !isFinished(r.status)) || (detail.value && !isFinished(detail.value.run.status))
  if (hasActive) listTimer = setInterval(() => { if (tab.value === 'runs') void loadRuns(runPage.value) }, 5000)
})

// ---- 重跑 / 停止 / 审批 ----
async function rerun(r: CIRun) {
  await ElMessageBox.confirm(`重跑 #${r.runNo}（${pipelineName(r.pipelineId)}）？将复用原版本与分支。`, '重跑', { type: 'warning' })
  const nr = await ciApi.rerun(r.id)
  ElMessage.success(`已触发重跑 #${nr.runNo}`)
  await loadRuns()
  void loadDetail(nr.id)
}
async function stopRun(r: CIRun) {
  await ElMessageBox.confirm(`停止 #${r.runNo}？进行中的任务会被终止。`, '停止', { type: 'warning' })
  await ciApi.cancel(r.id)
  ElMessage.success('已停止')
  await loadRuns()
  if (detail.value?.run.id === r.id) void loadDetail(r.id)
}
async function decide(t: CITaskRun, decision: 'approved' | 'rejected') {
  if (!detail.value) return
  if (decision === 'rejected') {
    await ElMessageBox.confirm('驳回将终止该流水线，确认？', '驳回', { type: 'warning' })
  }
  approving.value = t.id
  try {
    await ciApi.decideApproval(detail.value.run.id, t.nodeId, decision)
    ElMessage.success(decision === 'approved' ? '已通过' : '已驳回')
    await loadDetail(detail.value.run.id)
  } finally { approving.value = null }
}

// ---- 日志 ----
function openLogs(t: CITaskRun) {
  logTask.value = t
  logVisible.value = true
}

// ---- DAG 视图（按运行所用版本取 graph，节点按任务状态着色） ----
async function openDag() {
  const run = detail.value?.run
  if (!run?.versionId) return
  dagVisible.value = true
  dagLoading.value = true
  dagGraph.value = null
  try {
    const vs = await ciApi.pipelineVersions(run.pipelineId)
    const v = vs.find((x) => x.id === run.versionId)
    if (!v) { ElMessage.error('未找到该执行对应的流水线版本'); return }
    dagGraph.value = JSON.parse(v.graphJson) as CIGraph
  } catch (e: any) {
    ElMessage.error(e?.message || '获取版本 DSL 失败')
  } finally { dagLoading.value = false }
}
const dagFlow = computed(() => {
  const g = dagGraph.value
  if (!g?.nodes.length) return { nodes: [], edges: [] as any[] }
  const statusOf: Record<string, string> = {}
  for (const t of detail.value?.tasks ?? []) statusOf[t.nodeId] = t.status
  const STYLE: Record<string, { bg: string; border: string }> = {
    success: { bg: '#f6ffed', border: '#52c41a' },
    running: { bg: '#e6f4ff', border: '#1677ff' },
    failed: { bg: '#fff2f0', border: '#ff4d4f' },
    cancelled: { bg: '#fffbe6', border: '#faad14' },
    pending: { bg: '#fafafa', border: '#d9d9d9' },
  }
  const nodes = g.nodes.map((n) => {
    const st = STYLE[statusOf[n.id] ?? 'pending']
    return {
      id: n.id,
      type: 'dag',
      position: n.position ?? { x: 0, y: 0 },
      data: {
        label: n.type,
        style: { background: st.bg, border: `2px solid ${st.border}`, borderRadius: 8, padding: '8px 14px', fontSize: 12 },
      },
    }
  })
  const edges: any[] = g.edges.map((e: CIPEdge, i) => ({
    id: `e${i}`,
    source: e.source,
    target: e.target,
    type: 'smoothstep',
    label: e.branch ? (e.branch === 'yes' ? 'yes' : 'no') : undefined,
    markerEnd: { type: MarkerType.ArrowClosed },
  }))
  return { nodes, edges }
})

// ---- 运行 ----
async function openRun(p: CIPipeline) {
  runTarget.value = p
  runMode.value = 'branch'
  runRef.value = ''
  runRefs.branches = []; runRefs.tags = []; runRefs.err = undefined
  runRefs.def = 'main'
  runDlg.value = true
  try {
    const d = await ciApi.pipeline(p.id)
    const git = (d.graph?.nodes ?? []).find((n) => n.type === 'git-clone')
    const url = (git?.params?.url as string) || ''
    runRefs.def = (git?.params?.branch as string) || 'main'
    if (!/^https?:\/\//i.test(url)) {
      runRefs.err = '流水线未配置 git 仓库地址，按默认分支运行'
      return
    }
    const refs = await ciApi.repoRefs(url, (git?.params?.credential as string) || undefined)
    runRefs.branches = refs.branches || []
    runRefs.tags = refs.tags || []
  } catch (e: any) {
    runRefs.err = `分支列表加载失败（${e?.message || e}），可手输`
  }
}
async function doRun() {
  if (!runTarget.value) return
  running.value = true
  try {
    const refV = runRef.value.trim()
    const run = await ciApi.run(runTarget.value.id, refV ? { [runMode.value]: refV } : undefined)
    ElMessage.success(`已触发执行 #${run.runNo}${refV ? `（${runMode.value === 'branch' ? '分支' : 'tag'}: ${refV}）` : ''}`)
    runDlg.value = false
    tab.value = 'runs'
    await loadRuns(1)
    void loadDetail(run.id)
  } finally { running.value = false }
}

// ---- 流水线 CRUD ----
function openDesigner(p: CIPipeline) { router.push(`/ci/pipelines/${p.id}/design`) }
function openPipeDlg(p?: CIPipeline) {
  Object.assign(pipeForm, p
    ? { id: p.id, projectId: p.projectId, name: p.name, description: p.description || '' }
    : { id: undefined, projectId: filterProject.value, name: '', description: '' })
  pipeDlg.value = true
}
async function savePipe() {
  if (!pipeForm.name.trim()) { ElMessage.warning('名称必填'); return }
  if (!pipeForm.id && !pipeForm.projectId) { ElMessage.warning('请选择所属项目'); return }
  saving.value = true
  try {
    if (pipeForm.id) await ciApi.updatePipeline(pipeForm.id, { name: pipeForm.name, description: pipeForm.description })
    else await ciApi.createPipeline({ projectId: pipeForm.projectId!, name: pipeForm.name, description: pipeForm.description })
    ElMessage.success('已保存')
    pipeDlg.value = false
    await loadPipelines()
  } finally { saving.value = false }
}
async function dupPipe(p: CIPipeline) {
  if (!projects.value.length) { ElMessage.warning('当前集群还没有项目'); return }
  const options = projects.value.filter((x) => x.id !== p.projectId)
  if (!options.length) { ElMessage.warning('没有其他可复制到的项目'); return }
  const { value } = await ElMessageBox.prompt('选择复制到的项目（填项目 ID）', '复制流水线', {
    inputValue: String(options[0].id),
  })
  const target = Number(value)
  if (!target || !projects.value.some((x) => x.id === target)) { ElMessage.error('目标项目不存在'); return }
  await ciApi.duplicatePipeline(p.id, { targetProjectId: target })
  ElMessage.success('已复制')
  await loadPipelines()
}
async function delPipe(p: CIPipeline) {
  await confirmDelete(p.name, { title: '删除流水线', warning: '执行历史保留，进行中的执行会拒绝删除。' })
  await ciApi.deletePipeline(p.id)
  ElMessage.success('已删除')
  await loadPipelines()
}

// ---- 项目 CRUD ----
function openProjDlg(p?: CIProject) {
  Object.assign(projForm, p
    ? { id: p.id, name: p.name, displayName: p.displayName, namespace: p.namespace, description: p.description || '' }
    : { id: undefined, name: '', displayName: '', namespace: '', description: '' })
  projDlg.value = true
}
async function saveProj() {
  if (!projForm.name.trim()) { ElMessage.warning('项目标识必填'); return }
  if (!projForm.namespace.trim()) { ElMessage.warning('命名空间必填'); return }
  saving.value = true
  try {
    if (projForm.id) {
      await ciApi.updateProject(projForm.id, { displayName: projForm.displayName, description: projForm.description })
    } else {
      await ciApi.createProject({
        name: projForm.name, displayName: projForm.displayName,
        description: projForm.description, namespace: projForm.namespace,
      })
    }
    ElMessage.success('已保存')
    projDlg.value = false
    await loadProjects()
  } finally { saving.value = false }
}
async function delProj(p: CIProject) {
  await confirmDelete(p.name, { title: '删除项目', warning: `删除项目「${p.displayName || p.name}」，其下流水线需先清理，进行中的执行会拒绝删除。` })
  await ciApi.deleteProject(p.id)
  ElMessage.success('已删除')
  await loadProjects()
}

// ---- 凭据 CRUD ----
function openCredDlg(row?: any) {
  Object.assign(credForm, {
    id: row?.id, name: row?.name || '', form: row?.form || 'basic', type: row?.type || '',
    host: row?.extra?.host || '', username: '', password: '', token: '', dockerconfig: '',
    kubeconfig: '', accessKey: '', secretKey: '', region: '', endpoint: '',
    data: [], projectId: row?.projectId || undefined,
  })
  credDlg.value = true
}
async function saveCred() {
  if (!credForm.name.trim()) { ElMessage.warning('名称必填'); return }
  saving.value = true
  try {
    const body: Record<string, any> = {
      name: credForm.name, form: credForm.form, type: credForm.type || undefined,
      host: credForm.host || undefined, projectId: credForm.projectId || null,
    }
    switch (credForm.form) {
      case 'basic':
        body.username = credForm.username || undefined
        body.password = credForm.password || undefined
        break
      case 'token':
        body.token = credForm.token || undefined
        break
      case 'dockerconfig':
        body.username = credForm.username || undefined
        body.password = credForm.password || undefined
        body.dockerconfig = credForm.dockerconfig || undefined
        break
      case 'kubeconfig':
        body.kubeconfig = credForm.kubeconfig || undefined
        break
      case 'aksk':
        body.accessKey = credForm.accessKey || undefined
        body.secretKey = credForm.secretKey || undefined
        body.region = credForm.region || undefined
        body.endpoint = credForm.endpoint || undefined
        break
      case 'raw': {
        const data: Record<string, string> = {}
        for (const kv of credForm.data) if (kv.k.trim()) data[kv.k.trim()] = kv.v
        body.data = Object.keys(data).length ? data : undefined
        break
      }
    }
    if (credForm.id) await ciApi.updateCredential(credForm.id, body)
    else await ciApi.createCredential(body)
    ElMessage.success('已保存（K8s Secret 已同步）')
    credDlg.value = false
    await loadCredentials()
  } finally { saving.value = false }
}
async function delCred(row: any) {
  await confirmDelete(row.name, { title: '删除凭据', warning: '其 K8s Secret 一并删除；被流水线引用时会拒绝。' })
  await ciApi.deleteCredential(row.id)
  ElMessage.success('已删除')
  await loadCredentials()
}

// ---- 制品 / 发布记录 ----
const artifacts = ref<CIArtifact[]>([])
const artLoading = ref(false)
const filterArtProject = ref<number | undefined>()
const artPage = ref(1)
const artifactDlg = ref(false)
const artifactDetail = ref<CIArtifactDetail | null>(null)
const deployments = ref<CIDeployment[]>([])
const deployLoading = ref(false)
const filterDeployProject = ref<number | undefined>()
const deployPage = ref(1)

async function loadArtifacts(p = 1) {
  artPage.value = p
  artLoading.value = true
  try {
    const r = await ciApi.artifacts({ projectId: filterArtProject.value, page: p, size: 50 })
    artifacts.value = r.items ?? []
  } catch { artifacts.value = [] } finally { artLoading.value = false }
}
async function openArtifact(r: CIArtifact) {
  try { artifactDetail.value = await ciApi.artifact(r.id) } catch { artifactDetail.value = { ...r } }
  artifactDlg.value = true
}
function downloadArtifact(r: CIArtifact) {
  window.open(ciApi.artifactDownloadUrl(r.id), '_blank')
}
async function loadDeployments(p = 1) {
  deployPage.value = p
  deployLoading.value = true
  try {
    const r = await ciApi.deployments({ projectId: filterDeployProject.value, page: p, size: 50 })
    deployments.value = r.items ?? []
  } catch { deployments.value = [] } finally { deployLoading.value = false }
}
async function doRollback(r: CIDeployment) {
  await ElMessageBox.confirm(`按记录快照恢复镜像？（${r.namespace}：${(r.targets || []).map((t: any) => t.name).join(', ')}）`, '一键回滚', { type: 'warning' })
  try {
    const out = await ciApi.rollbackDeployment(r.id)
    ElMessage.success(out.status === 'success' ? '回滚完成' : '回滚部分失败，详见记录')
  } catch (e: any) {
    ElMessage.error(e?.message || '回滚失败')
  }
  await loadDeployments(deployPage.value)
}

// ---- 全局变量 ----
const globals = ref<CIGlobalVar[]>([])
const globalLoading = ref(false)
const globalDlg = ref(false)
const globalForm = reactive<{ id?: number; key: string; value: string; description: string }>({ key: '', value: '', description: '' })
async function loadGlobals() {
  globalLoading.value = true
  try { globals.value = await ciApi.globals() } catch { globals.value = [] } finally { globalLoading.value = false }
}
function openGlobalDlg(row?: CIGlobalVar) {
  Object.assign(globalForm, row ? { id: row.id, key: row.key, value: row.value, description: row.description } : { id: undefined, key: '', value: '', description: '' })
  globalDlg.value = true
}
async function saveGlobal() {
  if (!globalForm.key.trim()) { ElMessage.warning('Key 必填'); return }
  saving.value = true
  try {
    if (globalForm.id) await ciApi.updateGlobal(globalForm.id, { key: globalForm.key, value: globalForm.value, description: globalForm.description })
    else await ciApi.createGlobal({ key: globalForm.key, value: globalForm.value, description: globalForm.description })
    ElMessage.success('已保存')
    globalDlg.value = false
    await loadGlobals()
  } finally { saving.value = false }
}
async function delGlobal(row: CIGlobalVar) {
  await confirmDelete(row.key, { title: '删除全局变量', warning: '引用它的流水线将保留占位符原样运行。' })
  await ciApi.deleteGlobal(row.id)
  ElMessage.success('已删除')
  await loadGlobals()
}

// ---- 集群切换：整体重载 ----
watch(activeCluster, () => {
  invalidateNodeTypes()
  detail.value = null
  reloadAll()
})

// ---- URL query 同步（?tab= / ?focus= / ?project=） ----
function applyQuery() {
  const q = route.query
  if (typeof q.tab === 'string') tab.value = q.tab
  if (q.project) filterProject.value = Number(q.project)
  const focus = Number(q.focus)
  if (focus > 0) void loadDetail(focus)
}
watch(() => route.query, applyQuery)

onMounted(async () => {
  applyQuery()
  await loadReady()
  await loadProjects()
  loadCredentials()
  await loadPipelines()
  await loadRuns()
})
onBeforeUnmount(() => {
  ws?.close()
  if (detailTimer) clearInterval(detailTimer)
  if (listTimer) clearInterval(listTimer)
})

// 内联状态标签
const RunStatusTag = {
  props: { status: { type: String, default: '' } },
  setup(p: any) {
    const map: Record<string, [string, string]> = {
      pending: ['info', '等待中'], running: ['primary', '运行中'],
      success: ['success', '成功'], failed: ['danger', '失败'], cancelled: ['warning', '已取消'],
    }
    const [type, label] = map[p.status] ?? ['info', p.status || '未知']
    return () => h('el-tag', { size: 'small', type }, () => label)
  },
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.toolbar { display: flex; gap: 8px; margin-bottom: 10px; align-items: center; flex-wrap: wrap; }
.stat-row { display: flex; gap: 12px; margin-bottom: 14px; }
.stat-card { flex: 1; border: 1px solid var(--kc-border, #ebeef5); border-radius: 8px; padding: 10px 14px; text-align: center; }
.stat-num { font-size: 22px; font-weight: 700; }
.stat-card.ok .stat-num { color: #67c23a; }
.stat-card.bad .stat-num { color: #f56c6c; }
.stat-label { font-size: 12px; color: #909399; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.dim { color: #c0c4cc; }
.empty-tip { color: #909399; font-size: 13px; padding: 18px 0; text-align: center; }
.cursor-row { cursor: pointer; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; background: #c0c4cc; }
.dot.st-success { background: #67c23a; }
.dot.st-failed { background: #f56c6c; }
.dot.st-running { background: #409eff; }
.dot.st-pending { background: #c0c4cc; }
.dot.st-cancelled { background: #e6a23c; }
.sec-title { font-weight: 600; margin: 10px 0 8px; }
.task-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.task-name { font-weight: 600; }
.task-type { color: #909399; font-size: 12px; }
.task-dur { color: #909399; font-size: 12px; }
.task-results { margin-top: 8px; }
.logs-box { margin: 0; max-height: 62vh; overflow: auto; padding: 10px; background: #0d1117; color: #c9d1d9; font: 12px/1.6 ui-monospace, Consolas, monospace; white-space: pre-wrap; word-break: break-all; border-radius: 6px; }
.cfg-tip { color: #909399; font-size: 12px; }
.cred-kv { display: flex; gap: 6px; margin-bottom: 6px; align-items: center; }
.dag-box { height: 480px; border: 1px solid #ebeef5; border-radius: 8px; overflow: hidden; }
.dag-node { border-radius: 8px; font-size: 12px; white-space: nowrap; }
.dag-legend { margin-top: 10px; font-size: 12px; color: #909399; display: flex; gap: 6px; align-items: center; }
</style>
