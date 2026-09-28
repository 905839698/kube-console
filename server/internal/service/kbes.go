// kbes.go Elasticsearch 的索引管理与查询。
//
// 控制台不通集群内网络，且 ES 只有 HTTP 口，因此统一进 es 容器 curl 127.0.0.1:9200。
// 认证用容器内的 $ELASTIC_PASSWORD（脚本里展开，明文不经过控制台）；协议按容器自己的
// READINESS_PROBE_PROTOCOL（未开 security 时是 http，开了则是 https，配 -k 忽略自签）。
//
// 路径与查询体都是独立 argv / stdin 传给 curl，不拼进 shell 字符串。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kube-console/server/internal/kube"
)

// kbESIndexRe 索引名/别名：URL 路径安全字符集（含 . 与 _，便于看 .kibana 这类内部索引）
var kbESIndexRe = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,255}$`)

// kbESMaxSize 单次查询返回条数上限
const kbESMaxSize = 200

const kbESCurlScript = `path="$1"; shift
u="${ELASTICSEARCH_USERNAME:-elastic}:${ELASTIC_PASSWORD:-${ELASTIC_USER_PASSWORD:-}}"
p="${READINESS_PROBE_PROTOCOL:-http}"
exec curl -sS -k --max-time 25 -u "$u" "$@" "$p://127.0.0.1:9200$path"`

// KBESResult 一次 ES 调用的原始返回（JSON 能解析时另给结构体字段）
type KBESResult struct {
	Raw    string          `json:"raw"`
	Status int             `json:"status,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// kbESPod 找 ES 容器（组件名可能是 mdit/master/dit，容器统一叫 elasticsearch）
func kbESPod(ctx context.Context, c *kube.Client, ns, cluster string) (pod, container string, err error) {
	pods, err := KBPods(ctx, c, ns, cluster)
	if err != nil {
		return "", "", err
	}
	for _, p := range pods {
		for _, ct := range p.Containers {
			if strings.Contains(ct.Name, "elastic") {
				return p.Name, ct.Name, nil
			}
		}
	}
	return "", "", fmt.Errorf("该集群没有 elasticsearch 容器")
}

// kbESCall 不带请求体的一次 ES 调用（GET/DELETE 等）
func kbESCall(ctx context.Context, c *kube.Client, ns, cluster, method, path string, extra ...string) (KBESResult, error) {
	return kbESCallBody(ctx, c, ns, cluster, method, path, nil, extra...)
}

// kbESCallBody 在 es 容器里执行一次 curl：method + path 由调用方给，extra 是额外 curl 参数，
// body 走 stdin（--data-binary @-），不进 shell 字符串
func kbESCallBody(ctx context.Context, c *kube.Client, ns, cluster, method, path string, body []byte, extra ...string) (KBESResult, error) {
	pod, container, err := kbESPod(ctx, c, ns, cluster)
	if err != nil {
		return KBESResult{}, err
	}
	if strings.Contains(path, "..") {
		return KBESResult{}, fmt.Errorf("路径不合法")
	}
	args := append([]string{path, "-X", method}, extra...)
	var in io.Reader
	if len(body) > 0 {
		args = append(args, "--data-binary", "@-")
		in = bytes.NewReader(body)
	}
	out, errOut, err := kbExecInPod(ctx, c, ns, pod, container, in, kbWrapShell(kbESCurlScript, args...)...)
	res := KBESResult{Raw: strings.TrimSpace(out)}
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		if res.Raw == "" {
			return res, fmt.Errorf("%s", msg)
		}
		return res, fmt.Errorf("%s：%s", msg, res.Raw)
	}
	if json.Valid([]byte(res.Raw)) {
		res.Data = json.RawMessage(res.Raw)
	}
	return res, nil
}

// ---------- 健康与索引 ----------

type KBESHealth struct {
	Status         string `json:"status"`
	Cluster        string `json:"cluster"`
	Nodes          int    `json:"nodes"`
	DataNodes      int    `json:"dataNodes"`
	ActiveShards   int    `json:"activeShards"`
	Relocating     int    `json:"relocatingShards"`
	Unassigned     int    `json:"unassignedShards"`
	ActivePercent  string `json:"activeShardsPercent"`
	MaxTaskWaitSec int    `json:"maxTaskWaitMillis"`
}

// KBESClusterHealth GET /_cluster/health
func KBESClusterHealth(ctx context.Context, c *kube.Client, ns, cluster string) (*KBESHealth, error) {
	res, err := kbESCall(ctx, c, ns, cluster, "GET", "/_cluster/health")
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &raw); err != nil {
		return nil, err
	}
	in := func(k string) int {
		if v, ok := raw[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	return &KBESHealth{
		Status:        kbStr(raw["status"]),
		Cluster:       kbStr(raw["cluster_name"]),
		Nodes:         in("number_of_nodes"),
		DataNodes:     in("number_of_data_nodes"),
		ActiveShards:  in("active_shards"),
		Relocating:    in("relocating_shards"),
		Unassigned:    in("unassigned_shards"),
		ActivePercent: kbStr(raw["active_shards_percent_as_number"]),
	}, nil
}

func kbStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

type KBESIndex struct {
	Index       string `json:"index"`
	Health      string `json:"health"`
	Status      string `json:"status"`
	UUID        string `json:"uuid"`
	Primaries   int    `json:"primaries"`
	Replicas    int    `json:"replicas"`
	DocsCount   int64  `json:"docsCount"`
	DocsDeleted int64  `json:"docsDeleted"`
	StoreSize   string `json:"storeSize"`
	Segments    int64  `json:"segmentsCount"`
}

// KBESIndices GET /_cat/indices（按名排序，store 用人类可读单位）
func KBESIndices(ctx context.Context, c *kube.Client, ns, cluster string) ([]KBESIndex, error) {
	res, err := kbESCall(ctx, c, ns, cluster, "GET", "/_cat/indices?s=index&format=json&h=index,health,status,uuid,pri,rep,docs.count,docs.deleted,store.size,segments.count")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &rows); err != nil {
		return nil, fmt.Errorf("索引列表解析失败: %s", res.Raw)
	}
	out := make([]KBESIndex, 0, len(rows))
	num := func(m map[string]any, k string) int64 {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case string:
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
		return 0
	}
	for _, r := range rows {
		idx := KBESIndex{
			Index:       kbStr(r["index"]),
			Health:      kbStr(r["health"]),
			Status:      kbStr(r["status"]),
			UUID:        kbStr(r["uuid"]),
			StoreSize:   kbStr(r["store.size"]),
			Primaries:   int(num(r, "pri")),
			Replicas:    int(num(r, "rep")),
			DocsCount:   num(r, "docs.count"),
			DocsDeleted: num(r, "docs.deleted"),
			Segments:    num(r, "segments.count"),
		}
		out = append(out, idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// KBESIndexDetail 索引定义：分片数/副本数/别名/字段映射（一层展平）
type KBESIndexDetail struct {
	Settings map[string]string `json:"settings"`
	Aliases  []string          `json:"aliases"`
	Fields   []KBESField       `json:"fields"`
}

type KBESField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// KBESGetIndex GET /<index>
func KBESGetIndex(ctx context.Context, c *kube.Client, ns, cluster, index string) (*KBESIndexDetail, error) {
	if !kbESIndexRe.MatchString(index) {
		return nil, fmt.Errorf("索引名不合法")
	}
	res, err := kbESCall(ctx, c, ns, cluster, "GET", "/"+index)
	if err != nil {
		return nil, err
	}
	var wrap map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &wrap); err != nil {
		return nil, fmt.Errorf("索引定义解析失败: %s", res.Raw)
	}
	var body map[string]any
	for _, v := range wrap { // { "<索引名>": {...} }
		if m, ok := v.(map[string]any); ok {
			body = m
			break
		}
	}
	if body == nil {
		return nil, fmt.Errorf("没有拿到索引定义")
	}
	d := &KBESIndexDetail{Settings: map[string]string{}}
	if set, ok := body["settings"].(map[string]any); ok {
		flattenES(set, "", d.Settings)
	}
	if al, ok := body["aliases"].(map[string]any); ok {
		for k := range al {
			d.Aliases = append(d.Aliases, k)
		}
		sort.Strings(d.Aliases)
	}
	if mp, ok := body["mappings"].(map[string]any); ok {
		flattenESMapping(mp, "", &d.Fields)
	}
	return d, nil
}

// flattenES 把嵌套 settings 压成 "index.number_of_shards" = "1"
func flattenES(m map[string]any, prefix string, out map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := m[k].(map[string]any); ok {
			flattenES(sub, path, out)
			continue
		}
		out[path] = kbStr(m[k])
		if out[path] == "" {
			if b, err := json.Marshal(m[k]); err == nil {
				out[path] = string(b)
			}
		}
	}
}

// flattenESMapping 展平字段（properties 递归，其余取 type）
func flattenESMapping(m map[string]any, prefix string, out *[]KBESField) {
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		spec, ok := props[k].(map[string]any)
		if !ok {
			continue
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if t := kbStr(spec["type"]); t != "" {
			*out = append(*out, KBESField{Name: path, Type: t})
		}
		if _, has := spec["properties"]; has {
			flattenESMapping(spec, path, out)
		}
	}
}

// KBESDeleteIndex 删除索引（不可撤销，前端二次确认）
func KBESDeleteIndex(ctx context.Context, c *kube.Client, ns, cluster, index string) (string, error) {
	if !kbESIndexRe.MatchString(index) {
		return "", fmt.Errorf("索引名不合法")
	}
	if index == "*" || strings.HasPrefix(index, "*") {
		return "", fmt.Errorf("禁止通配删除")
	}
	res, err := kbESCall(ctx, c, ns, cluster, "DELETE", "/"+index)
	if err != nil {
		return res.Raw, err
	}
	return res.Raw, nil
}

// KBESearchInput 查询请求：DSL 原样透传，size 由服务端收口
type KBESearchInput struct {
	Index string          `json:"index"`
	Size  int             `json:"size"`
	Body  json.RawMessage `json:"body"`
}

// kbESUnwrapBody 取查询体字节：页面 DSL 是文本框内容，JSON 里就是一个字符串
// （`"body":"{\"query\":…}"`），也兼容调用方直接给对象。
// 早先只按对象解，导致查询页固定报 cannot unmarshal string into map。
func kbESUnwrapBody(raw json.RawMessage) ([]byte, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return nil, nil
	}
	if t[0] != '"' {
		return t, nil
	}
	var s string
	if err := json.Unmarshal(t, &s); err != nil {
		return nil, err
	}
	return bytes.TrimSpace([]byte(s)), nil
}

type KBESHits struct {
	Total   string           `json:"total"`
	TookMs  int              `json:"tookMs"`
	MaxSize int              `json:"maxSize"`
	Docs    []map[string]any `json:"docs"`
}

// KBESSearch POST /<index>/_search：返回总命中数与前 N 条文档
func KBESSearch(ctx context.Context, c *kube.Client, ns, cluster string, in *KBESearchInput) (*KBESHits, string, error) {
	idx := in.Index
	if idx == "" {
		idx = "*"
	}
	if !kbESIndexRe.MatchString(idx) {
		return nil, "", fmt.Errorf("索引名不合法")
	}
	size := in.Size
	if size <= 0 {
		size = 20
	}
	if size > kbESMaxSize {
		size = kbESMaxSize
	}
	body, err := kbESUnwrapBody(in.Body)
	if err != nil {
		return nil, "", fmt.Errorf("查询体不是合法 JSON: %v", err)
	}
	if len(body) == 0 {
		body = []byte(`{"query":{"match_all":{}}}`)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", fmt.Errorf("查询体需是一个 JSON 对象（整体写成 {\"query\":…}）: %v", err)
	}
	if m == nil {
		return nil, "", fmt.Errorf("查询体不能为 null")
	}
	m["size"] = size // 收口返回条数，避免一次拉爆浏览器
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	res, err := kbESCallBody(ctx, c, ns, cluster, "POST", "/"+idx+"/_search", payload, "-H", "Content-Type: application/json")
	if err != nil {
		return nil, res.Raw, err
	}
	var parsed struct {
		Took  int `json:"took"`
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits struct {
			Hits []struct {
				Index  string         `json:"_index"`
				ID     string         `json:"_id"`
				Score  *float64       `json:"_score"`
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(res.Raw), &parsed); err != nil {
		return nil, res.Raw, fmt.Errorf("查询结果不是预期结构（可能是 ES 报错）: %s", truncateStr(res.Raw, 300))
	}
	hits := &KBESHits{Total: strconv.Itoa(parsed.Total.Value), TookMs: parsed.Took, MaxSize: size, Docs: []map[string]any{}}
	for _, hd := range parsed.Hits.Hits {
		doc := map[string]any{"_index": hd.Index, "_id": hd.ID}
		if hd.Score != nil {
			doc["_score"] = *hd.Score
		}
		for k, v := range hd.Source {
			doc[k] = v
		}
		hits.Docs = append(hits.Docs, doc)
	}
	return hits, res.Raw, nil
}
