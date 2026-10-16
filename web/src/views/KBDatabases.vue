<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span class="title"><el-icon class="title-icon"><component :is="engineMeta.icon" /></el-icon>{{ engineLabel }}（KubeBlocks）</span>
        <div>
          <el-button :icon="Refresh" circle size="small" @click="load" />
          <el-button v-if="canCreate" type="primary" size="small" @click="openCreate">新建集群</el-button>
        </div>
      </div>
    </template>

    <el-alert v-if="notReady" type="warning" :closable="false" style="margin-bottom: 10px" :title="notReady" />
    <div class="scope-line">
      命名空间范围：<b>{{ scopeLabel }}</b>
      <span class="tip">（列表只显示你在这些命名空间有角色的集群；新建也只能落在这些命名空间）</span>
    </div>

    <el-table border :data="clusters" v-loading="loading" size="small" stripe>
      <el-table-column prop="namespace" label="命名空间" width="160" sortable />
      <el-table-column prop="name" label="集群" min-width="150" sortable />
      <el-table-column prop="engine" label="引擎" width="110" />
      <el-table-column label="拓扑" width="120">
        <template #default="{ row }">{{ row.topology || '独立组件' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }"><el-tag :type="phaseType(row.phase)" size="small">{{ row.phase || '-' }}</el-tag></template>
      </el-table-column>
      <el-table-column label="组件就绪" min-width="190">
        <template #default="{ row }">
          <el-tag v-for="c in row.components" :key="c.name" size="small" effect="plain" style="margin-right: 4px">
            {{ c.name }} {{ c.readyReplicas }}/{{ c.replicas }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="createdAt" label="创建时间" width="160" />
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="primary" @click="openDetail(row)">详情</el-button>
          <el-button size="small" text type="danger" :disabled="!canWriteNS(row.namespace)" @click="del(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新建向导：引擎 → 拓扑 → 组件资源 -->
    <!-- destroy-on-close：调度设置的行式编辑器每次打开重新挂载、从 sched 播种，
         关闭即销毁——否则上次会话的残留行会带进下一次新建 -->
    <el-dialog v-model="createDlg" title="新建数据库集群" width="720px" destroy-on-close>
      <el-form label-width="96px" size="small">
        <el-form-item label="命名空间" required>
          <el-select v-model="form.namespace" filterable style="width: 260px" :disabled="nsLocked">
            <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
          </el-select>
          <span class="tip" v-if="nsLocked">跟随顶栏命名空间选择</span>
        </el-form-item>
        <el-form-item label="引擎">
          <el-icon class="title-icon"><component :is="engineMeta.icon" /></el-icon>
          <el-input :model-value="engineLabel" disabled style="width: 200px" />
        </el-form-item>
        <el-form-item label="拓扑" required>
          <el-select v-model="form.topology" style="width: 260px" @change="onTopoChange">
            <el-option v-for="t in topologies" :key="t.name" :label="t.name + (t.default ? '（默认）' : '') + (t.shardings?.length ? '（分片）' : '')" :value="t.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="小写字母/数字/中划线，如 order-db" style="width: 300px" />
        </el-form-item>
        <el-form-item label="回收策略">
          <el-select v-model="form.terminationPolicy" style="width: 160px">
            <el-option v-for="t in ['Delete', 'WipeOut', 'Pause', 'None']" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item>
        <el-form-item label="组件" required>
          <el-table border :data="form.components" size="small" style="width: 100%">
            <el-table-column prop="name" :label="hasShardings ? '分片组' : '组件'" width="130" />
            <el-table-column v-if="hasShardings" label="分片数" width="130">
              <template #default="{ row }">
                <el-input-number v-if="row.kind === 'sharding'" v-model="row.shards" :min="row.shardsMin" :max="row.shardsMax || 128" size="small" />
                <span v-else class="tip">—</span>
              </template>
            </el-table-column>
            <el-table-column label="每片副本" width="110">
              <template #default="{ row }">
                <el-input-number v-model="row.replicas" :min="row.replicasMin" :max="row.replicasMax || 100" size="small" />
              </template>
            </el-table-column>
            <el-table-column label="版本" width="170">
              <template #default="{ row }">
                <el-select v-if="row.options.length" v-model="row.compDef" size="small" style="width: 100%" @change="onCompDefChange(row)">
                  <el-option v-for="o in row.options" :key="o.compDef" :label="o.serviceVersion || o.compDef" :value="o.compDef" />
                </el-select>
                <el-input v-else v-model="row.serviceVersion" size="small" placeholder="如 8.0.33" />
              </template>
            </el-table-column>
            <el-table-column label="CPU/内存上限" min-width="180">
              <template #default="{ row }">
                <el-input v-model="row.cpuLimit" size="small" placeholder="cpu 如 1" style="width: 48%" />
                <el-input v-model="row.memLimit" size="small" placeholder="内存 如 2Gi" style="width: 48%; margin-left: 4%" />
              </template>
            </el-table-column>
            <el-table-column label="存储" width="120">
              <template #default="{ row }">
                <el-input v-if="!row.ownsData" v-model="row.storage" size="small" placeholder="如 20Gi" />
                <el-tooltip v-else placement="top" content="该模板自带名为 data 的卷（emptyDir），配了存储会让集群卡在 Creating，所以这里不给填">
                  <span class="tip">模板自带</span>
                </el-tooltip>
              </template>
            </el-table-column>
          </el-table>
          <div class="tip">版本下拉即该引擎可用的 ComponentDefinition；副本范围与存储来自模板定义；分片拓扑的「分片数」上下限来自 ShardingDefinition 的 shardsLimit。未装 exporter 的组件不会有引擎指标。</div>
        </el-form-item>
        <el-form-item label="调度设置">
          <KbScheduling v-model="sched" />
          <div class="tip">对全部组件生效（<span class="mono">Cluster.spec.schedulingPolicy</span>）：专用 DB 节点带污点时，这里加容忍 + 节点标签，否则 Pod 会一直 Pending。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 详情 -->
    <el-drawer v-model="detailDlg" :title="`集群：${cur?.namespace}/${cur?.name}`" size="70%">
      <div v-if="cur">
        <div class="detail-head">
          <el-tag :type="phaseType(cur.phase)">{{ cur.phase }}</el-tag>
          <span class="meta">{{ cur.engine || cur.clusterDef }} · {{ cur.topology || '独立组件' }} · 回收 {{ cur.terminationPolicy }}</span>
          <el-tooltip v-if="cur.scheduling" placement="bottom" :content="cur.scheduling">
            <span class="meta sched-meta">调度：{{ cur.scheduling }}</span>
          </el-tooltip>
          <span class="meta" v-if="cur.message">{{ cur.message }}</span>
          <el-button size="small" @click="openConn">连接信息</el-button>
          <el-button size="small" style="margin-left: auto" @click="loadDetail">刷新</el-button>
        </div>
        <el-tabs v-model="tab">
          <el-tab-pane label="组件" name="comps">
            <el-table border :data="cur.components" size="small">
              <el-table-column prop="name" label="组件" width="150" />
              <el-table-column prop="compDef" label="模板" min-width="150" />
              <el-table-column prop="serviceVersion" label="版本" width="100" />
              <el-table-column label="就绪" width="90">
                <template #default="{ row }">{{ row.readyReplicas }}/{{ row.replicas }}</template>
              </el-table-column>
              <el-table-column prop="phase" label="状态" width="100" />
              <el-table-column label="资源" min-width="150">
                <template #default="{ row }">{{ row.cpuLimit || '-' }} / {{ row.memLimit || '-' }} / {{ row.storage || '-' }}</template>
              </el-table-column>
              <el-table-column prop="headlessService" label="Headless Service" min-width="190" show-overflow-tooltip />
            </el-table>
            <template v-if="cur.shardings?.length">
              <div class="sec-title">分片拓扑</div>
              <el-table border :data="cur.shardings" size="small">
                <el-table-column prop="name" label="分片" width="130" />
                <el-table-column prop="shardingDef" label="ShardingDef" min-width="170" />
                <el-table-column prop="shards" label="分片数" width="80" />
                <el-table-column prop="phase" label="状态" width="100" />
                <el-table-column label="成员组件" min-width="320">
                  <template #default="{ row }">
                    <el-tag v-for="m in row.components || []" :key="m.name" size="small" effect="plain"
                      :type="m.phase === 'Running' ? 'success' : 'warning'" style="margin-right: 4px">
                      {{ m.name }} {{ m.readyReplicas }}/{{ m.replicas }}
                    </el-tag>
                    <span v-if="!row.components?.length" class="tip">加载中或无成员</span>
                  </template>
                </el-table-column>
              </el-table>
            </template>
          </el-tab-pane>

          <el-tab-pane label="Pod" name="pods">
            <div class="ops-bar">
              <el-button
                v-for="e in cliEntries" :key="e.component" size="small" type="primary"
                @click="openCLI(e)"
              >{{ e.component }} 查询终端</el-button>
              <el-button size="small" @click="loadPods">刷新</el-button>
              <span class="tip">{{ cliEntries.length ? '查询终端用容器内的环境变量自动登录，明文密码不经过控制台' : '该引擎无内置查询 CLI，可用行内「终端」进容器' }}</span>
            </div>
            <el-table border :data="podList" size="small" v-loading="podsLoading" style="margin-top: 10px">
              <el-table-column prop="name" label="Pod" min-width="200" show-overflow-tooltip />
              <el-table-column prop="component" label="组件" width="120" />
              <el-table-column label="角色" width="100">
                <template #default="{ row }"><el-tag v-if="row.role" size="small" :type="row.role === 'primary' ? 'success' : 'info'">{{ row.role }}</el-tag><span v-else>-</span></template>
              </el-table-column>
              <el-table-column prop="ready" label="容器就绪" width="90" />
              <el-table-column prop="restarts" label="重启" width="70" />
              <el-table-column prop="phase" label="状态" width="90" />
              <el-table-column prop="podIP" label="Pod IP" width="130" />
              <el-table-column prop="node" label="节点" min-width="130" show-overflow-tooltip />
              <el-table-column label="容器" min-width="220">
                <template #default="{ row }">
                  <el-link
                    v-for="ct in row.containers" :key="ct.name" type="primary" style="margin-right: 10px"
                    :underline="false" @click="openShell(row, ct)"
                  >{{ ct.name }}{{ ct.ready ? '' : '（未就绪）' }}</el-link>
                </template>
              </el-table-column>
              <el-table-column prop="startTime" label="启动" width="150" />
            </el-table>
          </el-tab-pane>

          <el-tab-pane v-if="engine === 'kafka'" label="Topic 与消息" name="kafka">
            <KbKafka :cluster="cur" :writable="opsReady" />
          </el-tab-pane>

          <el-tab-pane v-if="engine === 'elasticsearch'" label="索引与查询" name="es">
            <KbEs :cluster="cur" :writable="opsReady" />
          </el-tab-pane>

          <el-tab-pane label="运维" name="ops">
            <div class="ops-bar">
              <el-select v-model="ops.component" placeholder="组件" size="small" style="width: 170px">
                <el-option v-for="c in cur.components" :key="c.name" :label="c.name" :value="c.name" />
              </el-select>
              <el-button size="small" :disabled="!opsReady" @click="doOps({ type: 'Restart' })">重启</el-button>
              <el-button size="small" :disabled="!opsReady" @click="doOps({ type: 'Stop' })">停止</el-button>
              <el-button size="small" :disabled="!opsReady" @click="doOps({ type: 'Start' })">启动</el-button>
              <el-divider direction="vertical" />
              <span class="meta">{{ ops.component ? `${ops.component} 当前 ${curReplicas || '?'} 副本` : '选组件' }}</span>
              <el-input-number v-model="ops.replicas" size="small" :min="1" style="width: 110px" />
              <el-button size="small" :disabled="!opsReady || !curReplicas || ops.replicas === curReplicas" @click="scale">扩缩容</el-button>
              <el-input v-model="ops.cpuLimit" size="small" placeholder="CPU" style="width: 80px" />
              <el-input v-model="ops.memLimit" size="small" placeholder="内存" style="width: 90px" />
              <el-button size="small" :disabled="!opsReady" @click="doOps({ type: 'VerticalScaling' })">改规格</el-button>
              <el-divider direction="vertical" />
              <el-button size="small" :disabled="!opsReady" @click="openExpand">扩磁盘</el-button>
              <el-button size="small" :disabled="!opsReady" @click="openUpgrade">版本升级</el-button>
              <el-button size="small" :disabled="!opsReady" @click="openSwitch">主备切换</el-button>
              <el-button size="small" :disabled="!opsReady" @click="openExpose">暴露服务</el-button>
            </div>
            <div class="tip" v-if="opsBusy" style="margin-top: 6px">
              该集群已有工单在执行：KubeBlocks 对同一集群串行处理运维，新下发的工单会停在 Pending 排队，不是没生效。
            </div>
            <el-table border :data="opsList" size="small" v-loading="opsLoading" style="margin-top: 10px">
              <el-table-column prop="createdAt" label="时间" width="160" />
              <el-table-column prop="name" label="工单" min-width="200" show-overflow-tooltip />
              <el-table-column prop="type" label="类型" width="140" />
              <el-table-column prop="component" label="组件" width="120" />
              <el-table-column label="进度" width="110">
                <template #default="{ row }"><el-tag size="small" :type="phaseType(row.phase)">{{ row.phase }} {{ row.progress }}</el-tag></template>
              </el-table-column>
              <el-table-column prop="message" label="信息" min-width="180" show-overflow-tooltip />
              <el-table-column label="操作" width="120" fixed="right">
                <template #default="{ row }">
                  <el-button size="small" text type="primary" @click="showOpsDetail(row)">详情</el-button>
                  <el-button size="small" text type="danger" :disabled="!opsReady" @click="cancelOps(row)">取消</el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="配置" name="config">
            <div class="ops-bar">
              <el-radio-group v-model="cfgMode" size="small">
                <el-radio-button value="params">参数表单</el-radio-button>
                <el-radio-button value="raw">原始文件</el-radio-button>
              </el-radio-group>
              <template v-if="cfgMode === 'params'">
                <el-select v-model="cfgComp" placeholder="组件" size="small" style="width: 170px" @change="loadCfgSchema">
                  <el-option v-for="c in cur.components" :key="c.name" :label="c.name" :value="c.name" />
                </el-select>
                <el-button size="small" type="primary" :disabled="!opsReady || !changedCount" :loading="cfgSubmitting" @click="submitParams">
                  提交 {{ changedCount ? `（${changedCount} 项）` : '' }}
                </el-button>
                <el-input v-model="cfgFilter" size="small" clearable placeholder="过滤参数名 / 说明" style="width: 200px" />
                <el-checkbox v-model="cfgOnlyChanged" size="small">只看已改</el-checkbox>
                <span class="tip" v-if="cfgSchema?.note">{{ cfgSchema.note }}</span>
                <span class="tip" v-else>动态参数热载生效；静态参数由 operator 滚动重启</span>
              </template>
              <template v-else>
                <el-select v-model="cfg.name" placeholder="选择配置载体" size="small" style="width: 340px" @change="loadCfg">
                  <el-option v-for="cm in cfgs" :key="cm.name" :label="cm.name" :value="cm.name" />
                </el-select>
                <el-button size="small" :disabled="!cfg.name || !opsReady" @click="saveCfg">保存</el-button>
                <span class="tip">直接改 ConfigMap 需自行重启组件才会生效；改参数请优先用「参数表单」</span>
              </template>
            </div>
            <template v-if="cfgMode === 'params'">
              <div v-loading="cfgLoading">
                <div v-for="f in cfgSchema?.files || []" :key="f.name" style="margin-top: 10px">
                  <div class="sec-title">
                    {{ f.name }}
                    <el-tag size="small" :type="f.reload === 'Dynamic' ? 'success' : f.reload === 'Script' ? 'warning' : 'info'" style="margin-left: 6px">
                      {{ f.reload === 'Dynamic' ? '动态热载' : f.reload === 'Script' ? '脚本热载' : '需重启' }}
                    </el-tag>
                    <span class="tip" style="margin-left: 8px">{{ shownParams(f).length }} / {{ f.params.length }} 项</span>
                  </div>
                  <!-- 高度按视口给：原来写死 420px，MySQL 几百个参数只能在小框里滚，抽屉下面全空着 -->
                  <el-table border :data="shownParams(f)" size="small" :max-height="paramTableMax">
                    <el-table-column prop="name" label="参数" min-width="210" show-overflow-tooltip>
                      <template #default="{ row }">
                        <span class="mono">{{ row.name }}</span>
                        <el-tooltip v-if="row.description" :content="row.description" placement="top"><el-icon class="help"><i>?</i></el-icon></el-tooltip>
                      </template>
                    </el-table-column>
                    <el-table-column label="类型" width="80">
                      <template #default="{ row }">
                        <span class="tip">{{ row.type || '-' }}</span>
                        <el-tag v-if="row.dynamic" size="small" type="success" effect="plain" style="margin-left: 4px">动</el-tag>
                      </template>
                    </el-table-column>
                    <el-table-column label="当前值" width="130">
                      <template #default="{ row }"><span class="mono">{{ row.current || '-' }}</span></template>
                    </el-table-column>
                    <el-table-column label="新值" min-width="170">
                      <template #default="{ row }">
                        <el-select v-if="row.enum?.length" v-model="edits[row.name]" size="small" clearable
                          :placeholder="row.current || '未设置'" style="width: 100%" :disabled="row.immutable">
                          <el-option v-for="e in row.enum" :key="e" :label="e" :value="e" />
                        </el-select>
                        <el-input v-else v-model="edits[row.name]" size="small" :disabled="row.immutable"
                          :placeholder="row.default ? `默认 ${row.default}` : row.current || '未设置'" />
                      </template>
                    </el-table-column>
                    <el-table-column label="范围" width="110">
                      <template #default="{ row }">
                        <span class="tip">{{ row.min || row.max ? `${row.min || '?'} ~ ${row.max || '?'}` : (row.enum?.join('/') || '-') }}</span>
                      </template>
                    </el-table-column>
                  </el-table>
                  <div v-if="!shownParams(f).length" class="tip" style="margin: 6px 0">
                    没有匹配的参数，换个关键词或去掉「只看已改」。
                  </div>
                </div>
              </div>
            </template>
            <template v-else>
              <el-input v-model="cfg.text" type="textarea" :rows="18" class="mono" placeholder="选择左侧配置载体后加载内容" style="margin-top: 8px" />
            </template>
          </el-tab-pane>

          <el-tab-pane label="备份" name="backup">
            <KbBackup :cluster="cur" :writable="opsReady" @changed="load" />
          </el-tab-pane>

          <el-tab-pane label="事件" name="events">
            <el-button size="small" :loading="eventsLoading" @click="loadEvents">刷新</el-button>
            <el-table border :data="events" size="small" style="margin-top: 10px" v-loading="eventsLoading">
              <el-table-column prop="lastTimestamp" label="时间" width="160" />
              <el-table-column prop="object" label="对象" min-width="200" show-overflow-tooltip />
              <el-table-column prop="reason" label="原因" width="140" />
              <el-table-column prop="count" label="次数" width="70" />
              <el-table-column prop="message" label="信息" min-width="280" show-overflow-tooltip />
            </el-table>
            <div v-if="!events.length && !eventsLoading" class="tip" style="margin-top: 8px">暂无告警事件</div>
          </el-tab-pane>

          <el-tab-pane label="账号" name="accounts">
            <el-table border :data="accounts" size="small">
              <el-table-column prop="name" label="Secret" min-width="240" />
              <el-table-column prop="type" label="类型" width="120" />
              <el-table-column prop="keys" label="键" min-width="220">
                <template #default="{ row }">{{ row.keys.join(', ') }}</template>
              </el-table-column>
            </el-table>
            <div class="tip">出于安全，控制台只列键名不显示明文；取密码请用 kubectl 或连接信息页。</div>
          </el-tab-pane>

          <el-tab-pane label="监控" name="monitor">
            <!-- 只在切到本页签时挂载：图表实例与时间序列查询都不该在背后常驻 -->
            <KbMetrics v-if="tab === 'monitor' && cur" :cluster="cur" :writable="opsReady" />
          </el-tab-pane>
        </el-tabs>
      </div>
    </el-drawer>

    <!-- 工单详情：条件时间线 + 变更前旧值 -->
    <el-dialog v-model="opsDetailDlg" :title="`工单：${opsCur?.name}`" width="640px">
      <template v-if="opsCur">
        <el-descriptions :column="3" border size="small">
          <el-descriptions-item label="类型">{{ opsCur.type }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag size="small" :type="phaseType(opsCur.phase)">{{ opsCur.phase }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="进度">{{ opsCur.progress || '-' }}</el-descriptions-item>
          <el-descriptions-item label="创建">{{ opsCur.createdAt }}</el-descriptions-item>
          <el-descriptions-item label="开始">{{ opsCur.startAt || '-' }}</el-descriptions-item>
          <el-descriptions-item label="完成">{{ opsCur.completedAt || '-' }}</el-descriptions-item>
        </el-descriptions>
        <div v-if="opsCur.message" class="err-box" style="margin-top: 10px">{{ opsCur.message }}</div>
        <div class="sec-title">条件时间线</div>
        <el-table border :data="opsCur.conditions" size="small">
          <el-table-column prop="lastTransition" label="时间" width="170" />
          <el-table-column prop="type" label="条件" width="150" />
          <el-table-column prop="status" label="状态" width="70" />
          <el-table-column prop="message" label="信息" min-width="220" show-overflow-tooltip />
        </el-table>
        <template v-if="opsCur.lastConfiguration && Object.keys(opsCur.lastConfiguration).length">
          <div class="sec-title">变更前的旧值（对照用）</div>
          <pre class="mono json-box">{{ JSON.stringify(opsCur.lastConfiguration, null, 2) }}</pre>
        </template>
      </template>
    </el-dialog>

    <!-- 扩磁盘 -->
    <el-dialog v-model="expandDlg" title="扩磁盘（VolumeExpansion）" width="480px">
      <div class="tip" style="margin-bottom: 8px">组件：{{ ops.component }}；卷名来自组件的 volumeClaimTemplates，只能扩不能缩</div>
      <el-table border :data="expandVols" size="small">
        <el-table-column prop="name" label="卷" width="120" />
        <el-table-column label="新容量" min-width="160">
          <template #default="{ row }"><el-input v-model="row.storage" size="small" placeholder="如 50Gi" /></template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="expandDlg = false">取消</el-button>
        <el-button type="primary" @click="doExpand">下发</el-button>
      </template>
    </el-dialog>

    <!-- 版本升级 -->
    <el-dialog v-model="upgradeDlg" title="版本升级（Upgrade）" width="480px">
      <el-form label-width="90px" size="small">
        <el-form-item label="组件">
          <el-select v-model="upgrade.component" style="width: 240px" @change="loadVersions">
            <el-option v-for="c in cur?.components || []" :key="c.name" :label="c.name" :value="c.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="当前版本">
          <span class="mono">{{ versionsOf(upgrade.component)?.current || cur?.components.find((c) => c.name === upgrade.component)?.serviceVersion || '-' }}</span>
        </el-form-item>
        <el-form-item label="目标版本">
          <el-select v-model="upgrade.version" style="width: 240px" placeholder="选择 ComponentVersion 里的版本">
            <el-option v-for="v in versionsOf(upgrade.component)?.candidates || []" :key="v" :label="v" :value="v" />
          </el-select>
          <div class="tip" v-if="!versionsOf(upgrade.component)?.candidates?.length">未找到升级链（ComponentVersion），无法升级</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="upgradeDlg = false">取消</el-button>
        <el-button type="primary" :disabled="!upgrade.version" @click="doUpgrade">下发</el-button>
      </template>
    </el-dialog>

    <!-- 主备切换 -->
    <el-dialog v-model="switchDlg" title="主备切换（Switchover）" width="440px">
      <el-form label-width="90px" size="small">
        <el-form-item label="组件">
          <el-select v-model="switchOp.component" style="width: 240px">
            <el-option v-for="c in cur?.components || []" :key="c.name" :label="c.name" :value="c.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="候选实例">
          <el-select v-model="switchOp.instance" style="width: 240px" clearable placeholder="留空由 operator 自选">
            <el-option v-for="p in podList.filter((x) => x.component === switchOp.component && x.role !== 'primary')"
              :key="p.name" :label="`${p.name}（${p.role || '-'}）`" :value="p.name" />
          </el-select>
          <div class="tip">把指定组件的主角色切换到候选实例</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="switchDlg = false">取消</el-button>
        <el-button type="primary" @click="doSwitch">下发</el-button>
      </template>
    </el-dialog>

    <!-- 暴露服务 -->
    <el-dialog v-model="exposeDlg" title="暴露服务（Expose）" width="440px">
      <el-form label-width="90px" size="small">
        <el-form-item label="组件">
          <el-select v-model="expose.component" style="width: 240px">
            <el-option v-for="c in cur?.components || []" :key="c.name" :label="c.name" :value="c.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="暴露方式">
          <el-select v-model="expose.serviceType" style="width: 240px">
            <el-option label="NodePort（节点端口）" value="NodePort" />
            <el-option label="LoadBalancer（外部 LB）" value="LoadBalancer" />
            <el-option label="ClusterIP（仅集群内）" value="ClusterIP" />
          </el-select>
        </el-form-item>
        <el-form-item label="服务名">
          <el-input v-model="expose.name" style="width: 240px" placeholder="np（生成 <集群>-<组件>-np）" />
        </el-form-item>
        <el-form-item label="开关">
          <el-switch v-model="expose.enable" active-text="Enable" inactive-text="Disable" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="exposeDlg = false">取消</el-button>
        <el-button type="primary" @click="doExpose">下发</el-button>
      </template>
    </el-dialog>

    <!-- 连接信息 -->
    <el-dialog v-model="connDlg" title="连接信息" width="720px">
      <template v-if="conn">
        <el-table border :data="conn.services" size="small">
          <el-table-column prop="component" label="组件" width="110" />
          <el-table-column prop="service" label="Service" min-width="150" show-overflow-tooltip />
          <el-table-column prop="type" label="类型" width="110" />
          <el-table-column label="内网地址" min-width="200">
            <template #default="{ row }"><span class="mono">{{ row.dns || '-' }}</span></template>
          </el-table-column>
          <el-table-column label="NodePort" width="120">
            <template #default="{ row }">
              <span v-for="p in row.ports.filter((x) => x.nodePort)" :key="p.name" class="mono" style="margin-right: 8px">{{ p.nodePort }}</span>
              <span v-if="!row.ports.some((x) => x.nodePort)" class="tip">-</span>
            </template>
          </el-table-column>
        </el-table>
        <div class="sec-title">账号（密码不回显）</div>
        <el-table border :data="conn.accounts" size="small">
          <el-table-column prop="component" label="组件" width="110" />
          <el-table-column prop="username" label="用户名" width="150" />
          <el-table-column prop="secret" label="密码所在 Secret" min-width="240" show-overflow-tooltip />
        </el-table>
        <div class="tip" style="margin-top: 8px">{{ conn.note }}</div>
      </template>
    </el-dialog>

    <WebTerminal
      v-model:visible="term.visible" :namespace="cur?.namespace || ''"
      :pod="term.pod" :container="term.container || undefined" :cmd="term.cmd || undefined"
    />
  </el-card>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { k8sApi } from '../api'
import WebTerminal from '../components/WebTerminal.vue'
import { kbEngineMeta } from '../utils/kb-engines'
import { confirmDelete } from '../utils/confirm'
import KbKafka from '../components/kb/KbKafka.vue'
import KbEs from '../components/kb/KbEs.vue'
import KbMetrics from '../components/kb/KbMetrics.vue'
import KbScheduling from '../components/kb/KbScheduling.vue'
import { emptyScheduling, kbSchedulingPayload, type KBSchedulingState } from '../utils/kb-scheduling'
import { useNamespaceStore } from '../store/namespace'
import { kbApi, kbMatchesEngine, type KBCluster, type KBCLIEntry, type KBCompOption, type KBContainer, type KBEngine, type KBOps, type KBOpsDetail, type KBComponentVersions, type KBConnection, type KBEvent, type KBPod, type KBConfigMapRef, type KBAccount, type KBConfigSchema, type KBTopology } from '../api/kb'
import KbBackup from '../components/kb/KbBackup.vue'
import { activeCluster } from '../store/clusterRef'
import { usePerm } from '../store/perm'

const perm = usePerm()
const route = useRoute()
const nsStore = useNamespaceStore()
const engine = computed(() => String(route.params.engine || 'mysql'))
// 标题/图标都取自 kb-engines（与左侧子菜单同一份定义，不会两处对不上）
const engineMeta = computed(() => kbEngineMeta(engine.value))
const engineLabel = computed(() => engineMeta.value.label)
// 顶栏命名空间作用域：__all__ = 全部（仍受权限过滤）
const scopeNs = computed(() => nsStore.selected.filter((n) => n !== '__all__'))
const allNs = computed(() => nsStore.selected.includes('__all__'))
const scopeLabel = computed(() => (allNs.value ? '全部命名空间（按权限）' : scopeNs.value.join(', ')))
const nsLocked = computed(() => !allNs.value && scopeNs.value.length === 1)
const notReady = computed(() => {
  if (loading.value) return ''
  if (!installed.value) return '当前集群未安装 KubeBlocks（或无 clusterdefinitions 权限），数据库功能不可用'
  if (!engines.value.some((e) => e.name === engine.value)) {
    return `当前集群没有 ${engineLabel.value} 的 KubeBlocks 引擎（ClusterDefinition 未安装），请先安装对应 addon`
  }
  return ''
})
const loading = ref(false)
const installed = ref(true)
const engines = ref<KBEngine[]>([])
const clusters = ref<KBCluster[]>([])
const nsOptions = ref<string[]>([])

const canCreate = computed(() => !!installed.value)
const canWriteNS = (ns: string) => perm.canWriteNS(ns)

async function load() {
  perm.load()
  loading.value = true
  try {
    const [eng, list] = await Promise.all([kbApi.engines(), kbApi.clusters()])
    engines.value = eng.engines || []
    installed.value = !!eng.installed
    allClusters.value = list || []
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}
const allClusters = ref<KBCluster[]>([])
// 只保留当前引擎 + 顶栏命名空间作用域内的集群
const scoped = () => allClusters.value.filter((x) => kbMatchesEngine(x, engine.value)
  && (allNs.value || scopeNs.value.includes(x.namespace)))
clusters.value = scoped()
watch([activeCluster, engine, () => nsStore.selected.join(',')], () => { clusters.value = scoped(); load() })
watch(allClusters, () => { clusters.value = scoped() })
load()

function phaseType(p: string) {
  if (p === 'Running') return 'success'
  if (p === 'Updating' || p === 'Creating') return 'warning'
  if (p === 'Stopped') return 'info'
  if (p === 'Failed' || p === 'Error') return 'danger'
  return 'info'
}

// ---------- 新建 ----------
const createDlg = ref(false)
const creating = ref(false)
// 新建向导的一行：普通拓扑是组件，分片拓扑是一个分片组（kind='sharding'，带分片数）
interface KBFormRow {
  kind: 'component' | 'sharding'
  name: string; compDef: string; options: KBCompOption[]; replicas: number
  replicasMin: number; replicasMax: number; serviceVersion: string; cpuLimit: string; memLimit: string; storage: string
  // 当前选中模板自带 data 卷（该行不该配存储）
  ownsData: boolean
  shardingDef: string; shards: number; shardsMin: number; shardsMax: number
}

const form = reactive<{
  namespace: string; clusterDef: string; topology: string; name: string; terminationPolicy: string;
  components: KBFormRow[]
}>({ namespace: '', clusterDef: '', topology: '', name: '', terminationPolicy: 'Delete', components: [] })

const hasShardings = computed(() => form.components.some((r) => r.kind === 'sharding'))

// 调度设置（亲和性 / 容忍污点 / 打散），每次开向导都从空开始：
// 上一个集群的污点设置留着会让人以为新集群也配了
const sched = ref<KBSchedulingState>(emptyScheduling())

const topologies = computed<KBTopology[]>(() => engines.value.find((e) => e.name === form.clusterDef)?.topologies || [])

async function openCreate() {
  let all: string[] = []
  try {
    all = (await k8sApi.namespaces()).map((n: any) => n.name)
  } catch { all = [] }
  // 候选 = 作用域内 ∩ 我有写权限的命名空间
  nsOptions.value = all.filter((n) => (allNs.value || scopeNs.value.includes(n)) && perm.canWriteNS(n))
  const def = engines.value.find((e) => e.name === engine.value)
  const topo = def?.topologies.find((t) => t.default) || def?.topologies[0]
  Object.assign(form, {
    namespace: nsLocked.value ? scopeNs.value[0] : (nsOptions.value[0] || ''),
    clusterDef: engine.value, topology: topo?.name || '', name: '', terminationPolicy: 'Delete', components: [],
  })
  sched.value = emptyScheduling()
  if (!nsOptions.value.length) {
    ElMessage.warning('没有可写的命名空间，无法新建（需要目标 ns 的 edit/admin 角色）')
    return
  }
  onTopoChange()
  createDlg.value = true
}
function rowBase(c: KBTemplateComponent, kind: 'component' | 'sharding'): KBFormRow {
  const options = c.compDefs || []
  const first = options.find((o) => o.compDef === c.compDef) || options[0]
  // 模板自带 data 卷（mongos 是 emptyDir）时这一格必须留空：同名卷一起下发，
  // KB 合成 InstanceSet 会失败，集群卡在 Creating 一个 Pod 都没有
  const ownsData = !!(first?.ownsDataVolume || c.ownsDataVolume)
  return {
    kind,
    name: c.name,
    compDef: first?.compDef || c.compDef,
    options,
    replicas: Math.max(1, c.replicasMin || 1),
    replicasMin: c.replicasMin || 1, replicasMax: c.replicasMax || 0, // 0 = 模板未声明上限
    serviceVersion: first ? first.serviceVersion || '' : c.serviceVersion || '',
    cpuLimit: '', memLimit: '', storage: !ownsData && c.volumes?.length ? '20Gi' : '',
    ownsData,
    shardingDef: '', shards: 0, shardsMin: 0, shardsMax: 0,
  }
}

function onTopoChange() {
  const t = topologies.value.find((x) => x.name === form.topology)
  const rows: KBFormRow[] = (t?.components || []).map((c) => rowBase(c, 'component'))
  for (const sh of t?.shardings || []) {
    // 一个分片组一行：模板来自 ShardingDefinition（可能给不出候选——addon 未装分片模板）
    for (const c of sh.components || []) {
      const r = rowBase(c, 'sharding')
      r.name = sh.name
      r.shardingDef = sh.shardingDef
      r.shardsMin = sh.shardsMin || 1
      r.shardsMax = sh.shardsMax || 0
      r.shards = r.shardsMin
      rows.push(r)
    }
    if (!(sh.components || []).length) {
      rows.push({
        ...rowBase({ name: sh.name, compDef: '', replicasMin: 1, replicasMax: 0, serviceVersion: '', volumes: [], hasExporter: false }, 'sharding'),
        shardingDef: sh.shardingDef, shardsMin: sh.shardsMin || 1, shardsMax: sh.shardsMax || 0, shards: sh.shardsMin || 1,
      })
    }
  }
  form.components = rows
}

// 切版本 = 换 ComponentDefinition（模板名与版本是一对一的，不能只改版本号）
function onCompDefChange(row: KBFormRow) {
  const picked = row.options.find((o) => o.compDef === row.compDef)
  row.serviceVersion = picked?.serviceVersion || ''
  // 换到的模板自带 data 卷就把存储清掉（留着会被后端挡下，报一个用户看不懂的错）
  if (picked?.ownsDataVolume) {
    row.ownsData = true
    row.storage = ''
  } else if (row.ownsData) {
    row.ownsData = false
  }
}
async function submitCreate() {
  if (!form.namespace || !form.name) {
    ElMessage.warning('命名空间与集群名称必填'); return
  }
  const shardRows = form.components.filter((r) => r.kind === 'sharding')
  const compRows = form.components.filter((r) => r.kind === 'component')
  if (!form.components.length) { ElMessage.warning('该拓扑没有可部署组件，换一个拓扑或检查引擎 addon'); return }
  if (shardRows.some((r) => !r.compDef)) { ElMessage.warning('分片模板没解析出可用的 ComponentDefinition，无法创建'); return }
  creating.value = true
  try {
    await kbApi.create(form.namespace, {
      name: form.name, clusterDef: form.clusterDef, topology: form.topology,
      terminationPolicy: form.terminationPolicy, components: compRows,
      scheduling: kbSchedulingPayload(sched.value),
      shardings: shardRows.map((r) => ({
        name: r.name, shardingDef: r.shardingDef, shards: r.shards, compDef: r.compDef,
        replicas: r.replicas, serviceVersion: r.serviceVersion, cpuLimit: r.cpuLimit, memLimit: r.memLimit, storage: r.storage,
      })),
    })
    ElMessage.success('已创建，正在拉起实例')
    createDlg.value = false
    load()
  } catch { /* 拦截器已提示 */ } finally { creating.value = false }
}

async function del(row: KBCluster) {
  // 删库是不可逆动作，且 terminationPolicy=Delete 时数据一起没了：只点一下"确定"
  // 太轻，必须完整输入集群名（与其它破坏性操作同一口径）
  try {
    await confirmDelete(row.name, {
      title: '删除数据库集群',
      warning: `${row.namespace} 下的 ${row.name} 将被删除；回收策略 ${row.terminationPolicy || '-'}${row.terminationPolicy === 'Delete' ? '（连 PV 与备份数据一起删）' : ''}；组件 ${row.components?.map((c) => c.name).join(' / ') || '-'}`,
    })
  } catch { return }
  await kbApi.remove(row.namespace, row.name)
  ElMessage.success('已提交删除')
  load()
}

// ---------- 详情 ----------
const detailDlg = ref(false)
const cur = ref<KBCluster | null>(null)
const tab = ref('comps')
// 路由只在参数间切换时复用本组件（router-view 的 key 是 K8s 集群名），
// 详情抽屉因此会跨引擎残留：在 Redis 页开过 redis-replication 的详情再点 Kafka 菜单，
// 抽屉还挂着旧集群，"Topic 与消息"页签一挂载就拿旧名去请求 kafka 接口 → 400。
// 切引擎即换了一批集群，抽屉直接关掉，详情只能从当前引擎的列表重新打开。
watch(engine, () => {
  if (!detailDlg.value) return
  detailDlg.value = false
  cur.value = null
  tab.value = 'comps'
})
const ops = reactive({ component: '', replicas: 1, cpuLimit: '', memLimit: '' })
// 当前选中组件的副本数：扩缩容的"从几到几"以此为准（详情刷新后跟着变）
const curReplicas = computed(() => cur.value?.components.find((c) => c.name === ops.component)?.replicas || 0)
// 工单排队提示：KB 同一集群同时只跑一个工单，新的会停在 Pending
const opsBusy = computed(() => opsList.value.some((o) => ['Pending', 'Creating', 'Running'].includes(o.phase)))
watch(() => ops.component, () => { if (curReplicas.value) ops.replicas = curReplicas.value })
const opsList = ref<KBOps[]>([])
const opsLoading = ref(false)
const cfgs = ref<KBConfigMapRef[]>([])
const cfg = reactive({ name: '', key: '', text: '' })
const accounts = ref<KBAccount[]>([])
const opsReady = computed(() => !!cur.value && perm.canWriteNS(cur.value.namespace))
const podList = ref<KBPod[]>([])
const podsLoading = ref(false)
const cliEntries = ref<KBCLIEntry[]>([])
const term = reactive({ visible: false, pod: '', container: '', cmd: '' })

function openShell(pod: KBPod, ct?: KBContainer) {
  Object.assign(term, { visible: true, pod: pod.name, container: ct?.name || '', cmd: '' })
}
// 查询终端 = 同一个 WebTerminal，只是把登录参数交给容器内的 shell 展开
function openCLI(e: KBCLIEntry) {
  Object.assign(term, { visible: true, pod: e.pod, container: e.container, cmd: e.command })
}
async function loadPods() {
  if (!cur.value) return
  const { namespace, name } = cur.value
  podsLoading.value = true
  try {
    const [pods, cli] = await Promise.all([kbApi.pods(namespace, name), kbApi.cli(namespace, name)])
    podList.value = pods
    cliEntries.value = cli.entries || []
  } catch { /* 拦截器已提示 */ } finally { podsLoading.value = false }
}

function openDetail(row: KBCluster) {
  cur.value = row
  detailDlg.value = true
  tab.value = 'comps'
  ops.component = row.components[0]?.name || ''
  ops.replicas = row.components[0]?.replicas || 1
  Object.assign(cfg, { name: '', key: '', text: '' })
  cfgMode.value = 'params'
  cfgComp.value = row.components[0]?.name || ''
  cfgSchema.value = null
  conn.value = null
  events.value = []
  podList.value = []
  cliEntries.value = []
  term.visible = false
  loadDetail()
}
async function loadDetail() {
  if (!cur.value) return
  const { namespace, name } = cur.value
  try {
    const [detail, opsL, cms, accs] = await Promise.all([
      kbApi.cluster(namespace, name), kbApi.ops(namespace, name),
      kbApi.configMaps(namespace, name), kbApi.accounts(namespace, name),
    ])
    cur.value = detail
    opsList.value = opsL
    cfgs.value = cms
    accounts.value = accs
    // 只接受属于本集群的配置名：列表变了就回落到第一个
    if (!cms.some((x) => x.name === cfg.name)) cfg.name = cms[0]?.name || ''
    if (cfgMode.value === 'raw') loadCfg()
    loadCfgSchema()
    // try/catch 会吞掉上面的同步异常：一旦某步抛错，排在其后的 loadPods/loadEvents
    // 就没机会执行，Pod/事件页签只能靠手动刷新——新增调用时别把会抛错的代码插到它们前面
    loadPods()
    loadEvents()
  } catch { /* 拦截器已提示 */ }
}
async function doOps(body: Record<string, any>) {
  if (!cur.value) return
  // 对话框类操作（升级/切换/暴露）已显式带 component，顶部下拉只是缺省值
  const r = await kbApi.submitOps(cur.value.namespace, cur.value.name, { component: ops.component, ...body })
  // KB 会排队：集群上已有工单在跑时新工单停在 Pending。不把 phase 说出来的话，用户看到的就是"点了没反应"
  const st = r?.phase ? `（状态 ${r.phase}${r.message ? '：' + r.message : ''}）` : ''
  ElMessage.success(`工单 ${r?.name || ''} 已下发${st}`)
  setTimeout(loadDetail, 1500)
}

// 扩缩容要让用户看清"从几到几"：目标与当前一致时不发空工单，只说明原因
async function scale() {
  if (!cur.value) return
  const from = curReplicas.value
  const to = ops.replicas
  if (!from) { ElMessage.warning('没读到该组件当前副本数，稍后再试'); return }
  if (to === from) { ElMessage.warning(`目标副本数与当前一致（${from}），无需下发工单`); return }
  try {
    await ElMessageBox.confirm(
      `组件 ${ops.component}：副本 ${from} → ${to}。` +
      (to > from ? '新增实例由 operator 加入复制拓扑并同步数据。' : '缩容会下线尾部实例，数据卷按集群回收策略处理。') +
      '确认下发 HorizontalScaling 工单？',
      '扩缩容确认', { type: 'warning', confirmButtonText: '下发工单' },
    )
  } catch { return }
  await doOps({ type: 'HorizontalScaling', replicas: to })
}

// ---------- 工单详情与取消 ----------
const opsDetailDlg = ref(false)
const opsCur = ref<KBOpsDetail | null>(null)
async function showOpsDetail(row: KBOps) {
  if (!cur.value) return
  try {
    opsCur.value = await kbApi.opsDetail(cur.value.namespace, cur.value.name, row.name)
    opsDetailDlg.value = true
  } catch { /* 拦截器已提示 */ }
}
async function cancelOps(row: KBOps) {
  if (!cur.value) return
  try {
    await ElMessageBox.confirm(`取消工单 ${row.name}？（${row.type}，operator 会在安全点回滚变更）`, '取消工单', { type: 'warning' })
  } catch { return }
  await kbApi.cancelOps(cur.value.namespace, cur.value.name, row.name)
  ElMessage.success('已请求取消')
  setTimeout(loadDetail, 1500)
}

// ---------- 扩磁盘 ----------
const expandDlg = ref(false)
const expandVols = reactive([{ name: 'data', storage: '' }])
function openExpand() {
  if (!ops.component) { ElMessage.warning('请选择组件'); return }
  expandVols[0].storage = ''
  expandDlg.value = true
}
async function doExpand() {
  if (!cur.value) return
  await doOps({ type: 'VolumeExpansion', volumes: expandVols.filter((v) => v.storage).map((v) => ({ ...v })) })
  expandDlg.value = false
}

// ---------- 版本升级 ----------
const upgradeDlg = ref(false)
const upgrade = reactive({ component: '', version: '' })
const compVersions = ref<KBComponentVersions[]>([])
const versionsOf = (comp: string) => compVersions.value.find((v) => v.component === comp)
async function loadVersions() {
  if (!cur.value || !upgrade.component) return
  try {
    compVersions.value = await kbApi.versions(cur.value.namespace, cur.value.name)
  } catch { compVersions.value = [] }
}
async function openUpgrade() {
  if (!ops.component) { ElMessage.warning('请选择组件'); return }
  upgrade.component = ops.component
  upgrade.version = ''
  await loadVersions()
  upgradeDlg.value = true
}
async function doUpgrade() {
  if (!cur.value || !upgrade.version) return
  await doOps({ type: 'Upgrade', component: upgrade.component, serviceVersion: upgrade.version })
  upgradeDlg.value = false
}

// ---------- 主备切换 ----------
const switchDlg = ref(false)
const switchOp = reactive({ component: '', instance: '' })
function openSwitch() {
  if (!ops.component) { ElMessage.warning('请选择组件'); return }
  switchOp.component = ops.component
  switchOp.instance = ''
  if (!podList.value.length) loadPods()
  switchDlg.value = true
}
async function doSwitch() {
  if (!cur.value) return
  await doOps({ type: 'Switchover', component: switchOp.component, instance: switchOp.instance })
  switchDlg.value = false
}

// ---------- 暴露服务 ----------
const exposeDlg = ref(false)
const expose = reactive({ component: '', serviceType: 'NodePort', name: 'np', enable: true })
function openExpose() {
  if (!ops.component) { ElMessage.warning('请选择组件'); return }
  expose.component = ops.component
  exposeDlg.value = true
}
async function doExpose() {
  if (!cur.value) return
  await doOps({ type: 'Expose', component: expose.component, serviceType: expose.serviceType, exposeName: expose.name, enable: expose.enable })
  exposeDlg.value = false
}

// ---------- 连接信息 ----------
const connDlg = ref(false)
const conn = ref<KBConnection | null>(null)
async function openConn() {
  if (!cur.value) return
  try {
    conn.value = await kbApi.connection(cur.value.namespace, cur.value.name)
    connDlg.value = true
  } catch { /* 拦截器已提示 */ }
}

// ---------- 事件 ----------
const events = ref<KBEvent[]>([])
const eventsLoading = ref(false)
async function loadEvents() {
  if (!cur.value) return
  eventsLoading.value = true
  try {
    events.value = await kbApi.events(cur.value.namespace, cur.value.name)
  } catch { events.value = [] } finally { eventsLoading.value = false }
}

// ---------- 参数表单（parameters.kubeblocks.io）----------
const cfgMode = ref<'params' | 'raw'>('params')
const cfgComp = ref('')
const cfgSchema = ref<KBConfigSchema | null>(null)
const cfgLoading = ref(false)
const cfgSubmitting = ref(false)
const edits = reactive<Record<string, string>>({})
// 参数过滤：MySQL 一个 my.cnf 就有几百个键，没有过滤框等于让人在 420px 的表里大海捞针
const cfgFilter = ref('')
const cfgOnlyChanged = ref(false)
// 抽屉里表格上方的固定占用（标题 / 状态行 / 页签 / 工具条 / 文件名）约 300px，
// 剩下的都给表格：一屏能看 20+ 行，下面不再空一片
const paramTableMax = 'calc(100vh - 300px)'

function shownParams(f: { params: { name: string; description?: string; current?: string }[] }) {
  const q = cfgFilter.value.trim().toLowerCase()
  return f.params.filter((p) => {
    if (cfgOnlyChanged.value && !isParamChanged(p)) return false
    if (!q) return true
    return p.name.toLowerCase().includes(q) || (p.description || '').toLowerCase().includes(q)
  })
}

function isParamChanged(p: { name: string; current?: string }) {
  const v = edits[p.name]
  return v !== undefined && v !== '' && v !== (p.current ?? '')
}
// 「改过了」的判定只有一处（isParamChanged）：提交按钮的计数与「只看已改」过滤
// 必须同一口径，否则勾上过滤看到的项可能不在提交数里，用户会以为漏提交了
const changedCount = computed(() => {
  if (!cfgSchema.value) return 0
  let n = 0
  for (const f of cfgSchema.value.files) {
    for (const p of f.params) {
      if (isParamChanged(p)) n++
    }
  }
  return n
})
async function loadCfgSchema() {
  if (!cur.value || !cfgComp.value) { cfgSchema.value = null; return }
  cfgLoading.value = true
  Object.keys(edits).forEach((k) => delete edits[k])
  try {
    cfgSchema.value = await kbApi.configSchema(cur.value.namespace, cur.value.name, cfgComp.value)
  } catch { cfgSchema.value = null } finally { cfgLoading.value = false }
}
async function submitParams() {
  if (!cur.value || !cfgComp.value) return
  const params: Record<string, string> = {}
  for (const f of cfgSchema.value?.files || []) {
    for (const p of f.params) {
      const v = edits[p.name]
      if (v !== undefined && v !== '' && v !== p.current) params[p.name] = v
    }
  }
  if (!Object.keys(params).length) { ElMessage.warning('没有要提交的修改'); return }
  cfgSubmitting.value = true
  try {
    await kbApi.submitReconfigure(cur.value.namespace, cur.value.name, cfgComp.value, params)
    ElMessage.success('已下发 Reconfiguring 工单，动态参数热载生效，静态参数由 operator 滚动重启')
    setTimeout(loadDetail, 1500)
    loadCfgSchema()
  } catch { /* 拦截器已提示 */ } finally { cfgSubmitting.value = false }
}
async function loadCfg() {
  if (!cur.value || !cfg.name) { cfg.text = ''; return }
  const d = await kbApi.getConfigMap(cur.value.namespace, cur.value.name, cfg.name)
  cfg.key = Object.keys(d.data)[0] || ''
  cfg.text = d.data[cfg.key] || ''
}
async function saveCfg() {
  if (!cur.value || !cfg.name) return
  const res = await kbApi.putConfigMap(cur.value.namespace, cur.value.name, cfg.name, { [cfg.key]: cfg.text })
  ElMessage.success(res?.note || '已保存')
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.title { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; }
.title-icon { font-size: 16px; color: var(--el-color-primary); }
.tip { font-size: 13px; color: var(--el-text-color-secondary); }
.scope-line { font-size: 13px; color: var(--el-text-color-regular); margin-bottom: 8px; }
.detail-head { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.meta { font-size: 13px; color: var(--el-text-color-regular); }
/* 调度摘要可能很长（多条容忍 + 亲和），截断并用 tooltip 看全文 */
.sched-meta { max-width: 420px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }
.ops-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 13px; }
.metric-line { font-size: 13px; }
.sec-title { font-weight: 600; margin: 12px 0 6px; font-size: 13px; }
.err-box { background: var(--kc-tint-coral); color: var(--kc-crit-text); padding: 8px 10px; border-radius: 4px; font-size: 13px; white-space: pre-wrap; }
.json-box { background: var(--el-fill-color-light); padding: 8px 10px; border-radius: 4px; max-height: 260px; overflow: auto; margin: 0; }
.help { font-style: normal; font-size: 13px; color: var(--el-text-color-secondary); cursor: help; margin-left: 4px; }
</style>
