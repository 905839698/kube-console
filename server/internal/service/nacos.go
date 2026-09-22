// Nacos 微服务集成：OpenAPI 客户端（v1 兼容 1.x/2.x、v3 兼容 3.x，自动探测）+ 命名空间自动同步器 +
// Pod 注入 Admission Webhook（自签 TLS、独立 HTTPS 端口、MutatingWebhookConfiguration 管理）
package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"gorm.io/gorm"

	"kube-console/server/internal/config"
	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// 注入到 Pod 的 envFrom 资源名（同步器在每个 ns 里维护）
const (
	NacosConfigMapName  = "nacos-config"
	NacosSecretName     = "nacos-credentials"
	NacosInjectLabel    = "nacos-injection"
	NacosInjectLabelVal = "enabled"
	WebhookConfigName   = "kube-console-nacos-inject"
)

// ------------------- Nacos OpenAPI 客户端（v1 兼容 1.x/2.x，v3 兼容 3.x，自动探测） -------------------

type NacosClient struct {
	addr     string
	username string
	password string
	http     *http.Client

	mu           sync.Mutex // token/v3/styleChecked 并发保护（sync 循环与手动同步共享客户端）
	token        string
	v3           bool // API 风格：false=v1（1.x/2.x），true=v3（3.x 移除了 v1 console/admin API）
	styleChecked bool // API 风格是否已探测
}

func NewNacosClient(cfg *model.NacosConfig) *NacosClient {
	return &NacosClient{
		addr:     strings.TrimRight(cfg.Addr, "/"),
		username: strings.TrimSpace(cfg.AdminUsername),
		password: cfg.AdminPassword,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

// do 执行 Nacos API 调用（form 提交），自动携带并缓存 accessToken；403/401 时重登一次
func (c *NacosClient) do(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	body, err := c.doOnce(ctx, method, path, form)
	if err == nil {
		return body, nil
	}
	// 凭证可能过期：重登一次再试（仅鉴权失败时）
	if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNKNOWN") {
		if c.username != "" {
			if lerr := c.login(ctx); lerr == nil {
				return c.doOnce(ctx, method, path, form)
			}
		}
	}
	return body, err
}

// httpDo 发送带凭证的请求：token 以 accessToken 参数 + Authorization 头双带（v1/v3 均识别），
// GET/DELETE 参数走 query，其余 form 提交；返回响应体与状态码（传输错误才返回 err）
func (c *NacosClient) httpDo(ctx context.Context, method, u string, form url.Values) ([]byte, int, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		if form == nil {
			form = url.Values{}
		}
		form = cloneForm(form)
		form.Set("accessToken", token)
	}
	var rdr io.Reader
	if form != nil && method != http.MethodGet && method != http.MethodDelete {
		rdr = strings.NewReader(form.Encode())
	} else if form != nil {
		u += "?" + form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func (c *NacosClient) doOnce(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	raw, status, err := c.httpDo(ctx, method, c.addr+path, form)
	if err != nil {
		return nil, fmt.Errorf("Nacos 不可达: %w", err)
	}
	if status >= 300 {
		// 500 足以覆盖 Spring 错误体 + 根因（如 MySQL Duplicate entry，通常出现在第 ~200 字符之后），
		// 过短会把幂等冲突关键词截掉（见 nacosAlreadyExists）
		return raw, fmt.Errorf("Nacos 返回 %d: %s", status, truncateStr(string(raw), 500))
	}
	return raw, nil
}

func cloneForm(src url.Values) url.Values {
	dst := url.Values{}
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}

// login 获取 accessToken（未配置管理账号 = 匿名模式，跳过）。v1/v3 登录响应结构相同（顶层 accessToken）。
func (c *NacosClient) login(ctx context.Context) error {
	if c.username == "" {
		return nil
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	path := "/nacos/v1/auth/users/login"
	if c.v3 {
		path = "/nacos/v3/auth/user/login"
	}
	raw, status, err := c.httpDo(ctx, http.MethodPost, c.addr+path, form)
	c.mu.Lock()
	isV3 := c.v3
	c.mu.Unlock()
	if status == http.StatusNotFound && !isV3 {
		// v1 登录不存在（v3-only 部署），切换 v3 重试
		c.mu.Lock()
		c.v3, c.styleChecked = true, true
		c.mu.Unlock()
		raw, status, err = c.httpDo(ctx, http.MethodPost, c.addr+"/nacos/v3/auth/user/login", form)
	}
	if err != nil {
		return fmt.Errorf("Nacos 登录失败: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("Nacos 登录返回 %d: %s", status, truncateStr(string(raw), 200))
	}
	var lr struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(raw, &lr) != nil || lr.AccessToken == "" {
		return fmt.Errorf("Nacos 登录响应无 accessToken: %s", truncateStr(string(raw), 200))
	}
	c.mu.Lock()
	c.token = lr.AccessToken
	c.mu.Unlock()
	return nil
}

func (c *NacosClient) ensureLogin(ctx context.Context) error {
	c.mu.Lock()
	needLogin := c.username != "" && c.token == ""
	c.mu.Unlock()
	if needLogin {
		if err := c.login(ctx); err != nil {
			return err
		}
	}
	c.detectStyle(ctx)
	return nil
}

// detectStyle 探测 Nacos 版本风格（每客户端一次）：3.x 移除了 v1 console API，
// v1 命名空间列表返回 404 即判定 v3；探测异常时保持 v1，由真实调用暴露错误。
func (c *NacosClient) detectStyle(ctx context.Context) {
	c.mu.Lock()
	if c.styleChecked {
		c.mu.Unlock()
		return
	}
	c.styleChecked = true
	c.mu.Unlock()
	if _, status, err := c.httpDo(ctx, http.MethodGet, c.addr+"/nacos/v1/console/namespaces", nil); err == nil && status == http.StatusNotFound {
		c.mu.Lock()
		c.v3 = true
		c.mu.Unlock()
	}
}

// nsParam v3 风格下空命名空间归一为 public（v3 默认命名空间 ID；v1 空串即 public）
func (c *NacosClient) nsParam(ns string) string {
	c.mu.Lock()
	v3 := c.v3
	c.mu.Unlock()
	if v3 && ns == "" {
		return "public"
	}
	return ns
}

// checkNacosResult 校验统一响应码：v1 成功 code=200，v3 成功 code=0；非 JSON 响应（v1 字面量）跳过
func checkNacosResult(raw []byte) error {
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Code == 0 || r.Code == 200 {
		return nil
	}
	return fmt.Errorf("Nacos 返回错误 %d: %s", r.Code, r.Message)
}

// doWrite 执行写操作并校验统一响应码
func (c *NacosClient) doWrite(ctx context.Context, method, path string, form url.Values) error {
	raw, err := c.do(ctx, method, path, form)
	if err != nil {
		return err
	}
	return checkNacosResult(raw)
}

type NacosNsInfo struct {
	Namespace         string `json:"namespace"`           // v1/v3 列表返回的真实 ID 字段
	NamespaceId       string `json:"namespaceId"`         // 部分版本字段名，回填供前端/匹配使用
	CustomNamespaceId string `json:"customNamespaceId"`
	NamespaceShowName string `json:"namespaceShowName"`
	NamespaceDesc     string `json:"namespaceDesc"`
	ConfigCount       int    `json:"configCount"`
}

// ID 取命名空间标识（不同版本字段名不一，取第一个非空）
func (n NacosNsInfo) ID() string {
	if n.Namespace != "" {
		return n.Namespace
	}
	if n.NamespaceId != "" {
		return n.NamespaceId
	}
	return n.CustomNamespaceId
}

func (c *NacosClient) ListNamespaces(ctx context.Context) ([]NacosNsInfo, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	path := "/nacos/v1/console/namespaces"
	if c.v3 {
		path = "/nacos/v3/admin/core/namespace/list"
	}
	raw, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if err := checkNacosResult(raw); err != nil {
		return nil, err
	}
	// v1/2.x console API 直接返回顶层数组（List<Namespace>），
	// v3 admin API 是 {"code":0,"data":[...]} 包裹——两种都认。
	// 旧实现只按 data 包裹解析，1.x/2.x 集群这里必然失败，
	// 连带命名空间同步/注入整体不可用
	var arr []NacosNsInfo
	if json.Unmarshal(raw, &arr) != nil {
		var r struct {
			Data []NacosNsInfo `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("解析 Nacos 命名空间失败: %w", err)
		}
		arr = r.Data
	}
	// v3 admin API 用蛇形字段（namespace_show_name/namespace_desc），
	// 驼形解析为空时按蛇形键回填展示名/描述
	type rawNs struct {
		ShowName string `json:"namespace_show_name"`
		Desc     string `json:"namespace_desc"`
	}
	rawTarget := (*[]rawNs)(nil)
	if err := json.Unmarshal(raw, &rawTarget); err == nil {
		// no-op：顶层数组形态
	} else {
		var r struct {
			Data []rawNs `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err == nil {
			rawTarget = &r.Data
		}
	}
	// 前端展示与存在性匹配统一走 namespaceId 字段，回填
	for i := range arr {
		if arr[i].NamespaceId == "" {
			arr[i].NamespaceId = arr[i].Namespace
		}
		if rawTarget != nil && i < len(*rawTarget) {
			if arr[i].NamespaceShowName == "" {
				arr[i].NamespaceShowName = (*rawTarget)[i].ShowName
			}
			if arr[i].NamespaceDesc == "" {
				arr[i].NamespaceDesc = (*rawTarget)[i].Desc
			}
		}
	}
	return arr, nil
}

func (c *NacosClient) CreateNamespace(ctx context.Context, id, name, desc string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	// v1 参数 customNamespaceId，v3 admin API 参数 namespaceId，其余一致
	if c.v3 {
		form := url.Values{"namespaceId": {id}, "namespaceName": {name}, "namespaceDesc": {desc}}
		return c.doWrite(ctx, http.MethodPost, "/nacos/v3/admin/core/namespace", form)
	}
	form := url.Values{"customNamespaceId": {id}, "namespaceName": {name}, "namespaceDesc": {desc}}
	return c.doWrite(ctx, http.MethodPost, "/nacos/v1/console/namespaces", form)
}

func (c *NacosClient) ListUsers(ctx context.Context) ([]string, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	path := "/nacos/v1/users"
	if c.v3 {
		path = "/nacos/v3/auth/user/list"
	}
	form := url.Values{"pageNo": {"1"}, "pageSize": {"500"}}
	raw, err := c.do(ctx, http.MethodGet, path, form)
	if err != nil {
		return nil, err
	}
	if err := checkNacosResult(raw); err != nil {
		return nil, err
	}
	type userItem struct {
		Username string `json:"username"`
	}
	// v1 顶层 pageItems；v3 嵌在 data.pageItems
	var r struct {
		PageItems []userItem `json:"pageItems"`
		Data      struct {
			PageItems []userItem `json:"pageItems"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil, nil // 老版本返回结构不同，忽略
	}
	items := r.PageItems
	if len(items) == 0 {
		items = r.Data.PageItems
	}
	out := make([]string, 0, len(items))
	for _, u := range items {
		out = append(out, u.Username)
	}
	return out, nil
}

// nacosAlreadyExists 判断 Nacos 错误是否为"已存在/已绑定"类幂等冲突，可安全忽略：
// v1/v3 应用层文案为 "already exist(s)"、"already bound to the role/resource"；
// v3 直连落库无应用层预检查时表现为数据库唯一约束冲突（MySQL "Duplicate entry"、Derby "duplicate key"）
func nacosAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{"already exist", "already bound to the role", "already bound to the resource", "duplicate entry", "duplicate key"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

func (c *NacosClient) CreateUser(ctx context.Context, username, password string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	path := "/nacos/v1/users"
	if c.v3 {
		path = "/nacos/v3/auth/user"
	}
	form := url.Values{"username": {username}, "password": {password}}
	if err := c.doWrite(ctx, http.MethodPost, path, form); err != nil {
		if nacosAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// UpdateUser 重置 Nacos 用户密码
func (c *NacosClient) UpdateUser(ctx context.Context, username, newPassword string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	path := "/nacos/v1/users"
	if c.v3 {
		path = "/nacos/v3/auth/user"
	}
	form := url.Values{"username": {username}, "newPassword": {newPassword}}
	return c.doWrite(ctx, http.MethodPut, path, form)
}

func (c *NacosClient) CreateRole(ctx context.Context, role, username string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	path := "/nacos/v1/roles"
	if c.v3 {
		path = "/nacos/v3/auth/role"
	}
	form := url.Values{"role": {role}, "username": {username}}
	if err := c.doWrite(ctx, http.MethodPost, path, form); err != nil {
		if nacosAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

func (c *NacosClient) GrantPermission(ctx context.Context, role, resource, action string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	path := "/nacos/v1/permissions"
	if c.v3 {
		path = "/nacos/v3/auth/permission"
	}
	form := url.Values{"role": {role}, "resource": {resource}, "action": {action}}
	if err := c.doWrite(ctx, http.MethodPost, path, form); err != nil {
		if nacosAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// NacosServiceInfo 服务发现视图（v1 老版本仅名称，计数为 0）
type NacosServiceInfo struct {
	Name         string `json:"name"`
	GroupName    string `json:"groupName"`
	ClusterCount int    `json:"clusterCount"`
	IpCount      int    `json:"ipCount"`      // 实例数
	HealthyCount int    `json:"healthyCount"` // 健康实例数
}

func (c *NacosClient) ListServices(ctx context.Context, namespaceId string) ([]NacosServiceInfo, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	path := "/nacos/v1/ns/service/list"
	if c.v3 {
		path = "/nacos/v3/admin/ns/service/list"
	}
	form := url.Values{"pageNo": {"1"}, "pageSize": {"500"}, "namespaceId": {c.nsParam(namespaceId)}}
	raw, err := c.do(ctx, http.MethodGet, path, form)
	if err != nil {
		return nil, err
	}
	if err := checkNacosResult(raw); err != nil {
		return nil, err
	}
	if c.v3 {
		var r struct {
			Data struct {
				PageItems []struct {
					Name                string `json:"name"`
					GroupName           string `json:"groupName"`
					ClusterCount        int    `json:"clusterCount"`
					IpCount             int    `json:"ipCount"`
					HealthyInstanceCount int   `json:"healthyInstanceCount"`
				} `json:"pageItems"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("解析服务列表失败: %w", err)
		}
		out := make([]NacosServiceInfo, 0, len(r.Data.PageItems))
		for _, s := range r.Data.PageItems {
			out = append(out, NacosServiceInfo{
				Name: s.Name, GroupName: s.GroupName,
				ClusterCount: s.ClusterCount, IpCount: s.IpCount, HealthyCount: s.HealthyInstanceCount,
			})
		}
		return out, nil
	}
	var r struct {
		Doms []string `json:"doms"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析服务列表失败: %w", err)
	}
	out := make([]NacosServiceInfo, 0, len(r.Doms))
	for _, d := range r.Doms {
		out = append(out, NacosServiceInfo{Name: d})
	}
	return out, nil
}

// NacosInstanceInfo 一个服务实例
type NacosInstanceInfo struct {
	InstanceId  string            `json:"instanceId"`
	Ip          string            `json:"ip"`
	Port        int               `json:"port"`
	Weight      float64           `json:"weight"`
	Healthy     bool              `json:"healthy"`
	Enabled     bool              `json:"enabled"`
	Ephemeral   bool              `json:"ephemeral"`
	ClusterName string            `json:"clusterName"`
	ServiceName string            `json:"serviceName"`
	Metadata    map[string]string `json:"metadata"`
}

func (c *NacosClient) ListInstances(ctx context.Context, namespaceId, groupName, serviceName string) ([]NacosInstanceInfo, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	if c.v3 {
		form := url.Values{"namespaceId": {c.nsParam(namespaceId)}, "groupName": {groupName}, "serviceName": {serviceName}}
		raw, err := c.do(ctx, http.MethodGet, "/nacos/v3/admin/ns/instance/list", form)
		if err != nil {
			return nil, err
		}
		if err := checkNacosResult(raw); err != nil {
			return nil, err
		}
		var r struct {
			Data []NacosInstanceInfo `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("解析实例列表失败: %w", err)
		}
		return r.Data, nil
	}
	form := url.Values{"namespaceId": {namespaceId}, "groupName": {groupName}, "serviceName": {serviceName}}
	raw, err := c.do(ctx, http.MethodGet, "/nacos/v1/ns/instance/list", form)
	if err != nil {
		return nil, err
	}
	var r struct {
		Hosts []NacosInstanceInfo `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析实例列表失败: %w", err)
	}
	return r.Hosts, nil
}

func (c *NacosClient) DeleteService(ctx context.Context, namespaceId, groupName, serviceName string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	var form url.Values
	if c.v3 {
		form = url.Values{"namespaceId": {c.nsParam(namespaceId)}, "groupName": {groupName}, "serviceName": {serviceName}}
		return c.doWrite(ctx, http.MethodDelete, "/nacos/v3/admin/ns/service", form)
	}
	form = url.Values{"namespaceId": {namespaceId}, "groupName": {groupName}, "serviceName": {serviceName}}
	return c.doWrite(ctx, http.MethodDelete, "/nacos/v1/ns/service", form)
}

type NacosConfigItem struct {
	ID        string `json:"id"`
	DataId    string `json:"dataId"`
	Group     string `json:"group"`
	GroupName string `json:"groupName"` // v3 字段名，展示时归一到 Group
	Type      string `json:"type"`
	Namespace string `json:"namespace,omitempty"` // 多命名空间合并展示用（handler 回填）
}

func (c *NacosClient) ListConfigs(ctx context.Context, namespaceId string) ([]NacosConfigItem, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	var (
		path string
		form url.Values
	)
	if c.v3 {
		path = "/nacos/v3/admin/cs/config/list"
		form = url.Values{"dataId": {""}, "groupName": {""}, "namespaceId": {c.nsParam(namespaceId)}, "pageNo": {"1"}, "pageSize": {"200"}}
	} else {
		path = "/nacos/v1/cs/configs"
		form = url.Values{"search": {"accurate"}, "dataId": {""}, "group": {""}, "pageNo": {"1"}, "pageSize": {"200"}, "tenant": {namespaceId}}
	}
	raw, err := c.do(ctx, http.MethodGet, path, form)
	if err != nil {
		return nil, err
	}
	if err := checkNacosResult(raw); err != nil {
		return nil, err
	}
	// v1 顶层 pageItems；v3 嵌在 data.pageItems（配置组字段 group → groupName）
	var r struct {
		PageItems []NacosConfigItem `json:"pageItems"`
		Data      struct {
			PageItems []NacosConfigItem `json:"pageItems"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析配置列表失败")
	}
	items := r.PageItems
	if len(items) == 0 {
		items = r.Data.PageItems
	}
	for i := range items {
		if items[i].Group == "" {
			items[i].Group = items[i].GroupName
		}
	}
	return items, nil
}

func (c *NacosClient) GetConfig(ctx context.Context, namespaceId, dataId, group string) (string, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return "", err
	}
	if c.v3 {
		form := url.Values{"dataId": {dataId}, "groupName": {group}, "namespaceId": {c.nsParam(namespaceId)}}
		raw, err := c.do(ctx, http.MethodGet, "/nacos/v3/admin/cs/config", form)
		if err != nil {
			return "", err
		}
		if err := checkNacosResult(raw); err != nil {
			return "", err
		}
		var r struct {
			Data struct {
				Content string `json:"content"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("解析配置内容失败: %w", err)
		}
		return r.Data.Content, nil
	}
	form := url.Values{"dataId": {dataId}, "group": {group}, "tenant": {namespaceId}}
	raw, err := c.do(ctx, http.MethodGet, "/nacos/v1/cs/configs", form)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *NacosClient) PublishConfig(ctx context.Context, namespaceId, dataId, group, content, typ string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	if c.v3 {
		form := url.Values{"dataId": {dataId}, "groupName": {group}, "namespaceId": {c.nsParam(namespaceId)}, "content": {content}, "type": {typ}}
		return c.doWrite(ctx, http.MethodPost, "/nacos/v3/admin/cs/config", form)
	}
	form := url.Values{"dataId": {dataId}, "group": {group}, "tenant": {namespaceId}, "content": {content}, "type": {typ}}
	return c.doWrite(ctx, http.MethodPost, "/nacos/v1/cs/configs", form)
}

func (c *NacosClient) DeleteConfig(ctx context.Context, namespaceId, dataId, group string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	if c.v3 {
		form := url.Values{"dataId": {dataId}, "groupName": {group}, "namespaceId": {c.nsParam(namespaceId)}}
		return c.doWrite(ctx, http.MethodDelete, "/nacos/v3/admin/cs/config", form)
	}
	form := url.Values{"dataId": {dataId}, "group": {group}, "tenant": {namespaceId}}
	return c.doWrite(ctx, http.MethodDelete, "/nacos/v1/cs/configs", form)
}

// Test 验证连通 + 凭证（登录 + 列命名空间）
func (c *NacosClient) Test(ctx context.Context) error {
	if _, err := c.ListNamespaces(ctx); err != nil {
		return err
	}
	return nil
}

// ------------------- 同步器 + Webhook 服务 -------------------

type NacosService struct {
	db       *gorm.DB
	clusters *ClusterManager
	cfg      config.NacosConfigYaml

	mu       sync.Mutex
	lastErr  map[string]string // cluster -> 最近一轮同步错误
	clients  map[string]*NacosClient
	certPEM  []byte
	keyPEM   []byte
}

func NewNacosService(db *gorm.DB, clusters *ClusterManager, cfg config.NacosConfigYaml) *NacosService {
	if cfg.SyncIntervalSec <= 0 {
		cfg.SyncIntervalSec = 300
	}
	if cfg.SkipNamespaces == "" {
		cfg.SkipNamespaces = "kube-system,kube-public,kube-node-lease,kube-console"
	}
	if cfg.CertDir == "" {
		cfg.CertDir = "./data"
	}
	return &NacosService{
		db: db, clusters: clusters, cfg: cfg,
		lastErr: map[string]string{},
		clients: map[string]*NacosClient{},
	}
}

// Start 启动 Webhook HTTPS 服务与同步循环
func (s *NacosService) Start() {
	// Webhook TLS（端口 0 = 不启动）
	if s.cfg.WebhookPort > 0 {
		go func() {
			certPEM, keyPEM, err := s.ensureCert()
			if err != nil {
				fmt.Printf("nacos webhook: 生成/加载证书失败: %v（Pod 注入不可用）\n", err)
				return
			}
			s.mu.Lock()
			s.certPEM, s.keyPEM = certPEM, keyPEM
			s.mu.Unlock()
			mux := http.NewServeMux()
			mux.HandleFunc("/inject/", s.handleInject)
			mux.HandleFunc("/inject", s.handleInject)
			srv := &http.Server{
				Addr:              ":" + strconv.Itoa(s.cfg.WebhookPort),
				Handler:           mux,
				TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
				ReadHeaderTimeout: 10 * time.Second,
			}
			fmt.Printf("nacos webhook: HTTPS 监听 :%d（/inject/{cluster}）\n", s.cfg.WebhookPort)
			if err := srv.ListenAndServeTLS(filepath.Join(s.cfg.CertDir, "nacos-webhook-cert.pem"), filepath.Join(s.cfg.CertDir, "nacos-webhook-key.pem")); err != nil {
				fmt.Printf("nacos webhook: 启动失败: %v\n", err)
			}
		}()
	}
	// 同步循环
	go func() {
		time.Sleep(15 * time.Second) // 等集群连接就绪
		s.syncAll()
		ticker := time.NewTicker(time.Duration(s.cfg.SyncIntervalSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.syncAll()
		}
	}()
}

func (s *NacosService) setErr(cluster, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg == "" {
		delete(s.lastErr, cluster)
	} else {
		s.lastErr[cluster] = truncateStr(msg, 300)
	}
}

// StatusOf 最近一轮同步状态（cluster -> 错误，空 = 正常）
func (s *NacosService) StatusOf() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.lastErr))
	for k, v := range s.lastErr {
		out[k] = v
	}
	return out
}

func (s *NacosService) clientFor(cfg *model.NacosConfig) *NacosClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[cfg.ClusterName]; ok {
		return c
	}
	c := NewNacosClient(cfg)
	s.clients[cfg.ClusterName] = c
	return c
}

// SyncNow 手动触发单集群同步（管理页「立即同步」）
func (s *NacosService) SyncNow(clusterName string) error {
	var cfg model.NacosConfig
	if err := s.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		return fmt.Errorf("该集群未配置 Nacos")
	}
	if err := s.syncCluster(&cfg); err != nil {
		s.setErr(clusterName, err.Error())
		return err
	}
	s.setErr(clusterName, "")
	return nil
}

// SyncNamespaceAsync 单命名空间即时同步（ns 创建钩子调用，异步不阻塞创建）
func (s *NacosService) SyncNamespaceAsync(clusterName, namespace string) {
	go func() {
		var cfg model.NacosConfig
		if err := s.db.Where("cluster_name = ? AND enabled = ?", clusterName, true).First(&cfg).Error; err != nil {
			return
		}
		if s.skipNamespace(namespace) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		client, err := s.clusters.Client(clusterName)
		if err != nil {
			return
		}
		if err := s.syncOneNamespace(ctx, client, s.clientFor(&cfg), &cfg, namespace, nil); err != nil {
			s.setErr(clusterName, err.Error())
			return
		}
		s.setErr(clusterName, "")
	}()
}

func (s *NacosService) skipNamespace(ns string) bool {
	for _, p := range strings.Split(s.cfg.SkipNamespaces, ",") {
		if strings.TrimSpace(p) == ns {
			return true
		}
	}
	return false
}

func (s *NacosService) syncAll() {
	var cfgs []model.NacosConfig
	s.db.Where("enabled = ?", true).Find(&cfgs)
	for i := range cfgs {
		if err := s.syncCluster(&cfgs[i]); err != nil {
			s.setErr(cfgs[i].ClusterName, err.Error())
			continue
		}
		s.setErr(cfgs[i].ClusterName, "")
	}
}

func (s *NacosService) syncCluster(cfg *model.NacosConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := s.clusters.Client(cfg.ClusterName)
	if err != nil {
		return err
	}
	nc := s.clientFor(cfg)

	nss, err := client.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	k8sNamespaces := map[string]bool{}
	for _, ns := range nss.Items {
		k8sNamespaces[ns.Name] = true
		if s.skipNamespace(ns.Name) {
			continue
		}
		// 已有映射（含凭据）则复用，没有则新开
		var mapping model.NacosNamespace
		have := s.db.Where("cluster_name = ? AND k8s_namespace = ?", cfg.ClusterName, ns.Name).First(&mapping).Error == nil
		var m *model.NacosNamespace
		if have {
			m = &mapping
		}
		if err := s.syncOneNamespace(ctx, client, nc, cfg, ns.Name, m); err != nil {
			return fmt.Errorf("ns %s: %w", ns.Name, err)
		}
		if !have {
			s.db.Where("cluster_name = ? AND k8s_namespace = ?", cfg.ClusterName, ns.Name).First(&mapping)
		} else {
			mapping.Status = "synced"
			mapping.Error = ""
			mapping.SyncedAt = time.Now()
			s.db.Save(&mapping)
		}
	}
	// K8s 已删除的 ns：标记保留（不删 Nacos 侧数据，防误删配置）
	var rows []model.NacosNamespace
	s.db.Where("cluster_name = ?", cfg.ClusterName).Find(&rows)
	for _, r := range rows {
		if !k8sNamespaces[r.K8sNamespace] && r.Status != "deleted-in-k8s" {
			s.db.Model(&r).Updates(map[string]interface{}{"status": "deleted-in-k8s", "synced_at": time.Now()})
		}
	}
	if cfg.InjectEnabled {
		if err := s.ensureWebhookConfig(ctx, client, cfg); err != nil {
			return fmt.Errorf("维护 MutatingWebhookConfiguration 失败: %w", err)
		}
	}
	return nil
}

// syncOneNamespace 单 ns：Nacos ns/用户/角色授权 + K8s ConfigMap/Secret + 注入标签
func (s *NacosService) syncOneNamespace(ctx context.Context, client *kube.Client, nc *NacosClient, cfg *model.NacosConfig, ns string, mapping *model.NacosNamespace) error {
	// 1. Nacos 命名空间
	nss, err := nc.ListNamespaces(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, n := range nss {
		if n.ID() == ns {
			exists = true
			break
		}
	}
	if !exists {
		if err := nc.CreateNamespace(ctx, ns, ns, "kube-console 自动创建"); err != nil {
			return fmt.Errorf("创建 Nacos 命名空间失败: %w", err)
		}
	}
	// 2. 用户（映射存在且已同步过 → 沿用已存密码；否则生成新随机密码）
	username := ns
	password := ""
	if mapping != nil && mapping.Password != "" {
		username, password = mapping.Username, mapping.Password
	} else {
		password, err = genPassword()
		if err != nil {
			return err
		}
	}
	users, err := nc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("查询 Nacos 用户失败: %w", err)
	}
	userExists := false
	for _, u := range users {
		if u == username {
			userExists = true
			break
		}
	}
	if !userExists {
		if err := nc.CreateUser(ctx, username, password); err != nil {
			return fmt.Errorf("创建 Nacos 用户失败: %w", err)
		}
	} else if mapping == nil {
		// 用户已存在但平台无凭据：重置为新生成密码（否则注入的密码无法登录）
		if err := nc.UpdateUser(ctx, username, password); err != nil {
			return fmt.Errorf("重置 Nacos 用户密码失败: %w", err)
		}
	}
	// 3. 角色授权（ROLE_<ns> 绑定用户，rw 权限绑定到该 ns 资源）
	role := "ROLE_" + ns
	if err := nc.CreateRole(ctx, role, username); err != nil {
		return fmt.Errorf("创建角色失败: %w", err)
	}
	// Nacos 权限 resource 是 namespace#group#dataId 三段式（支持 * 通配）：
	// 裸 ns 在鉴权匹配时永不命中（请求按 ns#group#dataId 组装 resource），
	// 注入的用户实际拿不到任何配置权限，应用 403
	if err := nc.GrantPermission(ctx, role, ns+"#*#*", "rw"); err != nil {
		return fmt.Errorf("授权失败: %w", err)
	}
	// 4. K8s 侧 ConfigMap / Secret（Pod 注入的数据源）
	if err := upsertNacosConfigMap(ctx, client, ns, cfg.Addr); err != nil {
		return err
	}
	if err := upsertNacosSecret(ctx, client, ns, username, password); err != nil {
		return err
	}
	// 5. 注入开关标签
	if cfg.AutoLabel {
		patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, NacosInjectLabel, NacosInjectLabelVal))
		_, _ = client.Clientset.CoreV1().Namespaces().Patch(ctx, ns, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	}
	// 6. 落库映射
	if mapping != nil {
		mapping.NacosNamespaceId = ns
		mapping.Username = username
		mapping.Password = password
		mapping.Status = "synced"
		mapping.Error = ""
		mapping.SyncedAt = time.Now()
		s.db.Save(mapping)
	} else {
		// 错误必须上抛：吞掉后下一轮同步仍走「用户已存在但 mapping==nil」分支
		// 重置密码——Nacos 侧密码每轮轮换，持有旧密码的客户端被持续踢掉
		if err := s.db.Create(&model.NacosNamespace{
			ClusterName: cfg.ClusterName, K8sNamespace: ns, NacosNamespaceId: ns,
			Username: username, Password: password, Status: "synced", SyncedAt: time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("Nacos 命名空间映射落库失败（Nacos 侧用户已就绪，修复后下轮自动对齐）: %w", err)
		}
	}
	return nil
}

func upsertNacosConfigMap(ctx context.Context, client *kube.Client, ns, addr string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: NacosConfigMapName, Namespace: ns}}
	want := map[string]string{"NACOS_SERVER_ADDR": addr, "NACOS_NAMESPACE": ns}
	existing, err := client.Clientset.CoreV1().ConfigMaps(ns).Get(ctx, NacosConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm.Data = want
		_, err = client.Clientset.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.Data["NACOS_SERVER_ADDR"] != addr || existing.Data["NACOS_NAMESPACE"] != ns {
		if existing.Data == nil {
			existing.Data = map[string]string{}
		}
		existing.Data["NACOS_SERVER_ADDR"] = addr
		existing.Data["NACOS_NAMESPACE"] = ns
		_, err = client.Clientset.CoreV1().ConfigMaps(ns).Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	return nil
}

func upsertNacosSecret(ctx context.Context, client *kube.Client, ns, username, password string) error {
	want := map[string]string{"NACOS_USERNAME": username, "NACOS_PASSWORD": password}
	existing, err := client.Clientset.CoreV1().Secrets(ns).Get(ctx, NacosSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: NacosSecretName, Namespace: ns},
			Type:       corev1.SecretTypeOpaque,
			StringData: want,
		}
		_, err = client.Clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.Data["NACOS_USERNAME"] != nil && string(existing.Data["NACOS_USERNAME"]) == username &&
		existing.Data["NACOS_PASSWORD"] != nil && string(existing.Data["NACOS_PASSWORD"]) == password {
		return nil
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data["NACOS_USERNAME"] = []byte(username)
	existing.Data["NACOS_PASSWORD"] = []byte(password)
	_, err = client.Clientset.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ResetNamespacePassword 轮换 ns 的 Nacos 用户密码，并同步 K8s Secret 与映射表
func (s *NacosService) ResetNamespacePassword(clusterName, ns string) (string, error) {
	var cfg model.NacosConfig
	if err := s.db.Where("cluster_name = ?", clusterName).First(&cfg).Error; err != nil {
		return "", fmt.Errorf("该集群未配置 Nacos")
	}
	var mapping model.NacosNamespace
	if err := s.db.Where("cluster_name = ? AND k8s_namespace = ?", clusterName, ns).First(&mapping).Error; err != nil {
		return "", fmt.Errorf("该命名空间尚未同步")
	}
	newPwd, err := genPassword()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.clientFor(&cfg).UpdateUser(ctx, mapping.Username, newPwd); err != nil {
		return "", fmt.Errorf("Nacos 侧重置密码失败: %w", err)
	}
	client, err := s.clusters.Client(clusterName)
	if err == nil {
		_ = upsertNacosSecret(ctx, client, ns, mapping.Username, newPwd)
	}
	mapping.Password = newPwd
	mapping.SyncedAt = time.Now()
	s.db.Save(&mapping)
	return mapping.Username, nil
}

// genPassword 生成 24 位随机密码（含大小写/数字，base64url）
func genPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ------------------- MutatingWebhookConfiguration 管理 -------------------

// CaBundlePEM 返回自签证书 PEM（供 MWC caBundle；证书未就绪时为空）
func (s *NacosService) CaBundlePEM() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.certPEM
}

func (s *NacosService) ensureCert() (certPEM, keyPEM []byte, err error) {
	certPath := filepath.Join(s.cfg.CertDir, "nacos-webhook-cert.pem")
	keyPath := filepath.Join(s.cfg.CertDir, "nacos-webhook-key.pem")
	if b, e := os.ReadFile(certPath); e == nil {
		if k, e2 := os.ReadFile(keyPath); e2 == nil {
			return b, k, nil
		}
	}
	// 自签证书（自身即 CA），SAN 覆盖常见 service 模式域名与本机
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "kube-console-webhook", Organization: []string{"kube-console"}},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:         true,
		DNSNames: []string{
			"localhost",
			"kube-console-server",
			"kube-console-server.kube-console",
			"kube-console-server.kube-console.svc",
			"kube-console-server.kube-console.svc.cluster.local",
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(s.cfg.CertDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// ensureWebhookConfig 维护集群的 MutatingWebhookConfiguration（caBundle 自动回填）
func (s *NacosService) ensureWebhookConfig(ctx context.Context, client *kube.Client, cfg *model.NacosConfig) error {
	caBundle := s.CaBundlePEM()
	if len(caBundle) == 0 {
		certPEM, _, err := s.ensureCert()
		if err != nil {
			return err
		}
		caBundle = certPEM
	}
	ignore := admissionregistrationv1.Ignore
	none := admissionregistrationv1.SideEffectClassNone
	scopeAll := admissionregistrationv1.AllScopes
	var timeout int32 = 5
	path := "/inject"
	if cfg.ClusterName != "" {
		path = "/inject/" + cfg.ClusterName
	}
	webhook := admissionregistrationv1.MutatingWebhook{
		Name: "nacos-inject.kube-console.io",
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
			Rule: admissionregistrationv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}, Scope: &scopeAll,
			},
		}},
		FailurePolicy:           &ignore,
		SideEffects:             &none,
		AdmissionReviewVersions: []string{"v1"},
		TimeoutSeconds:          &timeout,
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{NacosInjectLabel: NacosInjectLabelVal},
		},
	}
	webhook.ClientConfig.CABundle = caBundle
	if strings.EqualFold(cfg.WebhookMode, "service") {
		port := int32(cfg.WebhookServicePort)
		if port == 0 {
			port = 9443
		}
		webhook.ClientConfig.Service = &admissionregistrationv1.ServiceReference{
			Namespace: cfg.WebhookServiceNS,
			Name:      cfg.WebhookServiceName,
			Path:      &path,
			Port:      &port,
		}
	} else {
		u := strings.TrimSuffix(cfg.WebhookURL, "/")
		if !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("Webhook URL 必须以 https:// 开头（当前 %q）", cfg.WebhookURL)
		}
		full := u + path
		webhook.ClientConfig.URL = &full
	}

	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: WebhookConfigName},
		Webhooks:   []admissionregistrationv1.MutatingWebhook{webhook},
	}
	existing, err := client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, WebhookConfigName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, mwc, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Webhooks = mwc.Webhooks
	_, err = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// RemoveWebhookConfig 集群取消注入配置时清理 MWC（不存在则忽略）
func (s *NacosService) RemoveWebhookConfig(clusterName string) {
	client, err := s.clusters.Client(clusterName)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = client.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, WebhookConfigName, metav1.DeleteOptions{})
}

// ------------------- Admission Webhook 处理器 -------------------

func (s *NacosService) handleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cluster := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/inject"), "/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	review := &admissionv1.AdmissionReview{}
	if err := json.Unmarshal(body, review); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp := s.mutate(cluster, review)
	out, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// mutate 按 ns 查映射，给 Pod 的每个 container 追加 envFrom（幂等）
func (s *NacosService) mutate(cluster string, review *admissionv1.AdmissionReview) *admissionv1.AdmissionReview {
	resp := &admissionv1.AdmissionReview{
		TypeMeta: review.TypeMeta,
		Response: &admissionv1.AdmissionResponse{Result: &metav1.Status{}},
	}
	resp.Response.Allowed = true
	if review.Request == nil || review.Request.Resource.Resource != "pods" || cluster == "" {
		return resp
	}
	resp.Response.UID = review.Request.UID

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
		return resp
	}
	ns := review.Request.Namespace
	if ns == "" {
		ns = pod.Namespace
	}
	if ns == "" {
		return resp
	}
	// 双重校验：namespaceSelector 之外再确认映射存在且已同步
	var mapping model.NacosNamespace
	if err := s.db.Where("cluster_name = ? AND k8s_namespace = ? AND status = ?", cluster, ns, "synced").First(&mapping).Error; err != nil {
		return resp
	}
	_ = mapping

	var ops []map[string]interface{}
	refs := []map[string]interface{}{
		{"kind": "ConfigMap", "name": NacosConfigMapName},
		{"kind": "Secret", "name": NacosSecretName},
	}
	appendRef := func(containerIndex int, envFrom []corev1.EnvFromSource) {
		has := func(name string) bool {
			for _, ef := range envFrom {
				if (ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == name) || (ef.SecretRef != nil && ef.SecretRef.Name == name) {
					return true
				}
			}
			return false
		}
		missing := []map[string]interface{}{}
		for _, ref := range refs {
			if !has(ref["name"].(string)) {
				missing = append(missing, ref)
			}
		}
		if len(missing) == 0 {
			return
		}
		if len(envFrom) == 0 {
			ops = append(ops, map[string]interface{}{
				"op": "add", "path": fmt.Sprintf("/spec/containers/%d/envFrom", containerIndex), "value": missing,
			})
			return
		}
		for _, m := range missing {
			ops = append(ops, map[string]interface{}{
				"op": "add", "path": fmt.Sprintf("/spec/containers/%d/envFrom/-", containerIndex), "value": m,
			})
		}
	}
	for i := range pod.Spec.Containers {
		appendRef(i, pod.Spec.Containers[i].EnvFrom)
	}
	if len(ops) == 0 {
		return resp
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return resp
	}
	pt := admissionv1.PatchTypeJSONPatch
	resp.Response.PatchType = &pt
	resp.Response.Patch = patch
	return resp
}
