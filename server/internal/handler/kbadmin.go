// kbadmin.go 数据库集群的「进集群操作」接口：Pod 视图、镜像 CLI 终端入口、
// Kafka topic 管理、Elasticsearch 索引与查询。
//
// 权限口径与其它 KB 接口一致（按集群所在命名空间判定）：查看类要该 ns 可读，
// 变更类（建/删 topic、改配置、删索引）要该 ns 可写。
//
// 这些能力都得进 Pod 里的 CLI 才做得到（控制台不接集群内网络），因此统一走 pods/exec；
// 命令模板全部由服务端拼装，用户传的名字/JSON 只作为独立 argv 或 stdin 交给 CLI，
// 不拼进 shell 字符串——避免命令注入。
package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/service"
	"kube-console/server/pkg/response"
)

// Pods GET /kb/clusters/:namespace/:name/pods —— 集群实例 Pod（含角色与容器就绪）
func (h *KBHandler) Pods(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	pods, err := service.KBPods(c.Request.Context(), cli, ns, name)
	if err != nil {
		kbFail(c, err, "集群不存在")
		return
	}
	response.OK(c, pods)
}

// CLI GET /kb/clusters/:namespace/:name/cli —— 各组件镜像自带 CLI 的自动登录入口
func (h *KBHandler) CLI(c *gin.Context) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if h.denyRead(c, ns) {
		return
	}
	v, err := service.KBGetCluster(c.Request.Context(), cli, ns, name)
	if err != nil {
		kbFail(c, err, "集群不存在")
		return
	}
	entries, err := service.KBCLIs(c.Request.Context(), cli, ns, name, v.Engine)
	if err != nil {
		kbFail(c, err, "取 Pod 失败")
		return
	}
	response.OK(c, gin.H{"engine": v.Engine, "entries": entries})
}

// kbAdminTarget 公共前置：拿客户端 + ns/集群名 + 按读写分档鉴权（失败时已写响应）
func (h *KBHandler) kbAdminTarget(c *gin.Context, write bool) (*kube.Client, string, string, bool) {
	cli, _, ok := h.ctxClient(c)
	if !ok {
		return nil, "", "", false
	}
	ns, name := c.Param("namespace"), c.Param("name")
	if write && h.denyWrite(c, ns) {
		return nil, "", "", false
	}
	if !write && h.denyRead(c, ns) {
		return nil, "", "", false
	}
	return cli, ns, name, true
}

// ---------- Kafka ----------

// KafkaTopics GET /kb/clusters/:namespace/:name/kafka/topics
func (h *KBHandler) KafkaTopics(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	topics, err := service.KBKafkaTopics(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, topics)
}

// KafkaCreateTopic POST /kb/clusters/:namespace/:name/kafka/topics {name,partitions,replicationFactor,configs}
func (h *KBHandler) KafkaCreateTopic(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, true)
	if !ok {
		return
	}
	var in service.KBKafkaTopicInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	out, err := service.KBKafkaCreateTopic(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, 400, 400, out+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"result": out})
}

// KafkaAlterTopic PUT /kb/clusters/:namespace/:name/kafka/topics/:topic {set,unset}
func (h *KBHandler) KafkaAlterTopic(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, true)
	if !ok {
		return
	}
	var in struct {
		Set   map[string]string `json:"set"`
		Unset []string          `json:"unset"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	out, err := service.KBKafkaAlterConfigs(c.Request.Context(), cli, ns, name, c.Param("topic"), in.Set, in.Unset)
	if err != nil {
		response.Fail(c, 400, 400, out+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"result": out})
}

// KafkaDeleteTopic DELETE /kb/clusters/:namespace/:name/kafka/topics/:topic
func (h *KBHandler) KafkaDeleteTopic(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, true)
	if !ok {
		return
	}
	out, err := service.KBKafkaDeleteTopic(c.Request.Context(), cli, ns, name, c.Param("topic"))
	if err != nil {
		response.Fail(c, 400, 400, out+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"result": out})
}

// KafkaGroups GET /kb/clusters/:namespace/:name/kafka/groups
func (h *KBHandler) KafkaGroups(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	rows, err := service.KBKafkaGroups(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, rows)
}

// KafkaConsume POST /kb/clusters/:namespace/:name/kafka/consume {topic,count,fromBeginning}
func (h *KBHandler) KafkaConsume(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	var in service.KBKafkaConsumeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	out, err := service.KBKafkaConsume(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, 400, 400, strings.TrimSpace(out)+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"output": out})
}

// ---------- Elasticsearch ----------

// ESHealth GET /kb/clusters/:namespace/:name/es/health
func (h *KBHandler) ESHealth(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	res, err := service.KBESClusterHealth(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, res)
}

// ESIndices GET /kb/clusters/:namespace/:name/es/indices
func (h *KBHandler) ESIndices(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	rows, err := service.KBESIndices(c.Request.Context(), cli, ns, name)
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, rows)
}

// ESIndex GET /kb/clusters/:namespace/:name/es/indices/:index —— 定义（分片/别名/字段）
func (h *KBHandler) ESIndex(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	d, err := service.KBESGetIndex(c.Request.Context(), cli, ns, name, c.Param("index"))
	if err != nil {
		response.Fail(c, 400, 400, err.Error())
		return
	}
	response.OK(c, d)
}

// ESDeleteIndex DELETE /kb/clusters/:namespace/:name/es/indices/:index
func (h *KBHandler) ESDeleteIndex(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, true)
	if !ok {
		return
	}
	out, err := service.KBESDeleteIndex(c.Request.Context(), cli, ns, name, c.Param("index"))
	if err != nil {
		response.Fail(c, 400, 400, strings.TrimSpace(out)+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"result": out})
}

// ESSearch POST /kb/clusters/:namespace/:name/es/search {index,size,body}
func (h *KBHandler) ESSearch(c *gin.Context) {
	cli, ns, name, ok := h.kbAdminTarget(c, false)
	if !ok {
		return
	}
	var in service.KBESearchInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	hits, raw, err := service.KBESSearch(c.Request.Context(), cli, ns, name, &in)
	if err != nil {
		response.Fail(c, 400, 400, strings.TrimSpace(raw)+" "+err.Error())
		return
	}
	response.OK(c, gin.H{"hits": hits, "raw": raw})
}
