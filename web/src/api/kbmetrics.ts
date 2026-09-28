// 数据库集群的内置指标面板。PromQL 模板收两个选择器：
//   sel —— exporter 序列按组件 headless service 精确匹配（没接 ServiceMonitor 就没数据）
//   pod —— 该集群全部实例 Pod（容器资源类指标用它，不依赖 exporter）
export interface KBMetricDef {
  title: string
  promql: (sel: string, pod: string) => string
  unit?: string
  group: string
}

const eng = (title: string, promql: (s: string) => string, unit?: string): KBMetricDef =>
  ({ title, group: '引擎指标', unit, promql: (s) => promql(s) })
const res = (title: string, promql: (p: string) => string, unit?: string): KBMetricDef =>
  ({ title, group: '资源', unit, promql: (_s, p) => promql(p) })

// 容器/Pod 资源指标（cAdvisor + kube-state-metrics），各引擎共用
const RES_METRICS: KBMetricDef[] = [
  res('CPU 使用', (p) => `sum(rate(container_cpu_usage_seconds_total{${p},container!=""}[2m])) by (pod)`, '核'),
  res('内存工作集', (p) => `sum(container_memory_working_set_bytes{${p},container!=""}) by (pod)`, 'B'),
  res('网络收', (p) => `sum(rate(container_network_receive_bytes_total{${p}}[2m])) by (pod)`, 'B/s'),
  res('网络发', (p) => `sum(rate(container_network_transmit_bytes_total{${p}}[2m])) by (pod)`, 'B/s'),
  res('磁盘写', (p) => `sum(rate(container_fs_writes_bytes_total{${p},container!=""}[2m])) by (pod)`, 'B/s'),
  res('容器重启(1h)', (p) => `sum(increase(kube_pod_container_status_restarts_total{${p}}[1h])) by (pod)`, '次'),
  res('Pod 未就绪数', (p) => `count(kube_pod_status_ready{${p},condition="false"} == 1) by (pod)`),
]

// 指标名来源：mysql / postgresql / elasticsearch 三套在本集群 Prometheus 里逐个核对过；
// redis(oliver006) / kafka(kahuming exporter) / mongodb 用各自 exporter 的固定名，未在本集群验证
export const KB_METRICS: Record<string, KBMetricDef[]> = {
  mysql: [
    eng('QPS（语句/秒）', (s) => `rate(mysql_global_status_questions{${s}}[2m])`, '/s'),
    eng('当前连接', (s) => `mysql_global_status_threads_connected{${s}}`),
    eng('连接上限', (s) => `mysql_global_variables_max_connections{${s}}`),
    eng('拒绝连接(15m)', (s) => `increase(mysql_global_status_aborted_connects{${s}}[15m])`, '次'),
    eng('运行中线程', (s) => `mysql_global_status_threads_running{${s}}`),
    eng('慢查询(15m)', (s) => `increase(mysql_global_status_slow_queries{${s}}[15m])`, '次'),
    eng('InnoDB 读盘比例', (s) => `rate(mysql_global_status_innodb_buffer_pool_reads{${s}}[5m]) / rate(mysql_global_status_innodb_buffer_pool_read_requests{${s}}[5m])`),
    eng('InnoDB 行操作', (s) => `sum(rate(mysql_global_status_innodb_row_ops_total{${s}}[2m])) by (operation)`, '/s'),
    eng('InnoDB 行锁等待', (s) => `mysql_global_status_innodb_row_lock_current_waits{${s}}`),
    eng('行锁累计(15m)', (s) => `increase(mysql_global_status_innodb_row_lock_waits{${s}}[15m])`, '次'),
    eng('临时表落盘', (s) => `rate(mysql_global_status_created_tmp_disk_tables{${s}}[5m])`, '/s'),
    eng('打开的表', (s) => `mysql_global_status_open_tables{${s}}`),
    eng('入流量', (s) => `rate(mysql_global_status_bytes_received{${s}}[2m])`, 'B/s'),
    eng('出流量', (s) => `rate(mysql_global_status_bytes_sent{${s}}[2m])`, 'B/s'),
    eng('主从延迟', (s) => `mysql_slave_status_seconds_behind_master{${s}}`, 's'),
    eng('半同步副本状态', (s) => `mysql_global_status_rpl_semi_sync_replica_status{${s}}`),
    eng('运行时长', (s) => `mysql_global_status_uptime{${s}}`, 's'),
    ...RES_METRICS,
  ],
  redis: [
    eng('存活', (s) => `redis_up{${s}}`),
    eng('客户端连接', (s) => `redis_connected_clients{${s}}`),
    eng('阻塞客户端', (s) => `redis_blocked_clients{${s}}`),
    eng('拒绝连接(15m)', (s) => `increase(redis_rejected_connections_total{${s}}[15m])`, '次'),
    eng('OPS', (s) => `rate(redis_commands_processed_total{${s}}[2m])`, '/s'),
    eng('实时 OPS', (s) => `redis_instantaneous_ops_per_sec{${s}}`),
    eng('命中率', (s) => `rate(redis_keyspace_hits_total{${s}}[5m]) / (rate(redis_keyspace_hits_total{${s}}[5m]) + rate(redis_keyspace_misses_total{${s}}[5m]))`),
    eng('逐出键(15m)', (s) => `increase(redis_evicted_keys_total{${s}}[15m])`, '个'),
    eng('过期键(15m)', (s) => `increase(redis_expired_keys_total{${s}}[15m])`, '个'),
    eng('内存使用', (s) => `redis_memory_used_bytes{${s}}`, 'B'),
    eng('内存上限', (s) => `redis_memory_max_bytes{${s}}`, 'B'),
    eng('内存碎片率', (s) => `redis_memory_fragmentation_ratio{${s}}`),
    eng('已连副本', (s) => `redis_connected_slaves{${s}}`),
    eng('主从链路', (s) => `redis_master_link_up{${s}}`),
    eng('各库键数', (s) => `sum(redis_db_keys{${s}}) by (db)`),
    eng('运行时长', (s) => `redis_uptime_in_seconds{${s}}`, 's'),
    ...RES_METRICS,
  ],
  postgresql: [
    eng('活动连接（按状态）', (s) => `sum(pg_stat_activity_count{${s}}) by (state)`),
    eng('后端数', (s) => `sum(pg_stat_database_numbackends{${s}}) by (datname)`),
    eng('提交速率', (s) => `sum(rate(pg_stat_database_xact_commit{${s}}[2m])) by (datname)`, '/s'),
    eng('回滚速率', (s) => `sum(rate(pg_stat_database_xact_rollback{${s}}[2m])) by (datname)`, '/s'),
    eng('死锁(15m)', (s) => `sum(increase(pg_stat_database_deadlocks{${s}}[15m])) by (datname)`, '次'),
    eng('临时文件写', (s) => `sum(rate(pg_stat_database_temp_bytes{${s}}[5m])) by (datname)`, 'B/s'),
    eng('缓存命中率', (s) => `sum(rate(pg_stat_database_blks_hit{${s}}[5m])) / (sum(rate(pg_stat_database_blks_hit{${s}}[5m])) + sum(rate(pg_stat_database_blks_read{${s}}[5m])))`),
    eng('库大小', (s) => `sum(pg_database_size_bytes{${s}}) by (datname)`, 'B'),
    eng('复制延迟', (s) => `pg_replication_lag_seconds{${s}}`, 's'),
    eng('锁（按模式）', (s) => `sum(pg_locks_count{${s}}) by (mode)`),
    eng('max_connections', (s) => `pg_settings_max_connections{${s}}`),
    eng('运行时长', (s) => `time() - pg_postmaster_start_time_seconds{${s}}`, 's'),
    ...RES_METRICS,
  ],
  kafka: [
    eng('Broker 序列数', (s) => `count(kafka_brokers{${s}})`),
    eng('Topic 数', (s) => `count(count by (topic) (kafka_topic_partitions{${s}}))`),
    eng('分区数', (s) => `sum(kafka_topic_partitions{${s}})`),
    eng('各 Topic 末尾位点', (s) => `max(kafka_topic_partition_current_offset{${s}}) by (topic)`),
    eng('生产速率', (s) => `sum(rate(kafka_topic_partition_current_offset{${s}}[5m])) by (topic)`, '/s'),
    eng('ISR 不足分区', (s) => `sum(kafka_topic_partition_under_replicated_partition{${s}})`),
    eng('消费组 Lag', (s) => `sum(kafka_consumergroup_lag{${s}}) by (consumergroup, topic)`),
    eng('消费组成员', (s) => `sum(kafka_consumergroup_members{${s}}) by (consumergroup)`),
    ...RES_METRICS,
  ],
  elasticsearch: [
    eng('集群节点数', (s) => `elasticsearch_cluster_health_number_of_nodes{${s}}`),
    eng('未分配分片', (s) => `elasticsearch_cluster_health_unassigned_shards{${s}}`),
    eng('迁移中分片', (s) => `elasticsearch_cluster_health_relocating_shards{${s}}`),
    eng('待处理任务', (s) => `elasticsearch_cluster_health_number_of_pending_tasks{${s}}`),
    eng('文档数', (s) => `sum(elasticsearch_indices_docs{${s}})`),
    eng('已删文档', (s) => `sum(elasticsearch_indices_docs_deleted{${s}})`),
    eng('存储大小', (s) => `sum(elasticsearch_indices_store_size_bytes{${s}})`, 'B'),
    eng('段数', (s) => `sum(elasticsearch_indices_segments_count{${s}})`),
    eng('段内存', (s) => `sum(elasticsearch_indices_segments_memory_bytes{${s}})`, 'B'),
    eng('搜索速率', (s) => `sum(rate(elasticsearch_indices_search_query_total{${s}}[5m]))`, '/s'),
    eng('平均搜索耗时', (s) => `sum(rate(elasticsearch_indices_search_query_time_seconds{${s}}[5m])) / sum(rate(elasticsearch_indices_search_query_total{${s}}[5m]))`, 's'),
    eng('取回速率', (s) => `sum(rate(elasticsearch_indices_search_fetch_total{${s}}[5m]))`, '/s'),
    eng('写入速率', (s) => `sum(rate(elasticsearch_indices_indexing_index_total{${s}}[5m]))`, '/s'),
    eng('合并中', (s) => `sum(elasticsearch_indices_merges_current{${s}})`),
    eng('线程池活跃', (s) => `sum(elasticsearch_thread_pool_active_count{${s}}) by (name)`),
    eng('线程池排队', (s) => `sum(elasticsearch_thread_pool_queue_count{${s}}) by (name)`),
    eng('线程池拒绝(15m)', (s) => `sum(increase(elasticsearch_thread_pool_rejected_count{${s}}[15m])) by (name)`, '次'),
    eng('熔断(15m)', (s) => `sum(increase(elasticsearch_breakers_tripped{${s}}[15m])) by (breaker)`, '次'),
    eng('进程 CPU', (s) => `elasticsearch_process_cpu_percent{${s}}`, '%'),
    eng('常驻内存', (s) => `elasticsearch_process_mem_resident_size_bytes{${s}}`, 'B'),
    eng('磁盘可用', (s) => `elasticsearch_filesystem_data_available_bytes{${s}}`, 'B'),
    eng('传输收', (s) => `sum(rate(elasticsearch_transport_rx_size_bytes_total{${s}}[2m]))`, 'B/s'),
    ...RES_METRICS,
  ],
  mongodb: [
    eng('存活', (s) => `mongodb_up{${s}}`),
    eng('OPcounters', (s) => `sum(rate(mongodb_ss_opcounters{${s}}[2m])) by (legacyOpType)`, '/s'),
    eng('当前连接', (s) => `mongodb_ss_connections{${s},conn_type="current"}`),
    eng('文档读写', (s) => `sum(rate(mongodb_ss_metrics_document{${s}}[2m])) by (doc_op_type)`, '/s'),
    eng('常驻内存', (s) => `mongodb_mem_resident_megabytes{${s}}`, 'MB'),
    eng('副本成员健康', (s) => `mongodb_mongod_replset_member_health{${s}}`),
    ...RES_METRICS,
  ],
}

// 资源组不依赖 exporter，任何引擎都能算；未装对应 exporter 时引擎指标会整列空掉
export const KB_METRIC_GROUP_RESOURCE = '资源'
