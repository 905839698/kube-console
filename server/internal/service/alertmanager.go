// Alertmanager 访问客户端：默认经集群内 Service DNS 直连（控制台与 AM 同集群，省去
// apiserver service proxy 转发这一跳），跨集群接入填 DirectURL；两跳都不可达时
// 自动回退 apiserver 代理。支持实时告警 / 静默管理 / 主配置 YAML 读写（Secret 内 alertmanager.yaml[.gz]）
package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"kube-console/server/internal/kube"
	"kube-console/server/internal/model"
)

// AMAlert Alertmanager /api/v2/alerts 条目
type AMAlert struct {
	Fingerprint  string            `json:"fingerprint"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Value        string            `json:"value"`
	Status       struct {
		State       string   `json:"state"` // active | suppressed | unprocessed
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
}

// AMSilence Alertmanager /api/v2/silences 条目
type AMSilence struct {
	ID        string      `json:"id"`
	Matchers  []AMMatcher `json:"matchers"`
	StartsAt  string      `json:"startsAt"`
	EndsAt    string      `json:"endsAt"`
	CreatedBy string      `json:"createdBy"`
	Comment   string      `json:"comment"`
	Status    struct {
		State string `json:"state"` // expired | active | pending
	} `json:"status"`
}

// AMMatcher 静默匹配器（AM v2 的 name/value/isRegex/isEqual 四元组）
type AMMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// amRequest 访问 Alertmanager，按跳依次尝试，连接失败（无响应）自动换下一跳，
// 拿到 HTTP 响应（含 5xx）即按结果返回不再重试：
//  1. DirectURL（跨集群接入显式指定）或集群内 Service DNS 直连（默认，同集群免转发）；
//  2. kube-apiserver service proxy（直连不可达时的兜底，透传 apiserver 凭证）。
func amRequest(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig, method, path string, body []byte) ([]byte, error) {
	path = strings.TrimLeft(path, "/")
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}},
	}
	type hop struct {
		base  string
		proxy bool
		label string
	}
	var hops []hop
	if cfg.DirectURL != "" {
		hops = append(hops, hop{strings.TrimRight(cfg.DirectURL, "/"), false, "直连地址"})
	} else {
		// 默认集群内 svc 直连：控制台与 AM 同集群（跨集群请填 DirectURL）
		hops = append(hops, hop{
			fmt.Sprintf("http://%s.%s.svc:%d", cfg.Service, cfg.Namespace, cfg.Port),
			false, "集群内 svc 直连",
		})
	}
	hops = append(hops, hop{
		fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%d/proxy",
			strings.TrimRight(c.Config.Host, "/"), cfg.Namespace, cfg.Service, cfg.Port),
		true, "apiserver 代理",
	})
	var lastErr error
	for _, h := range hops {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, h.base+"/"+path, rdr)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		// svc/直连无认证头；代理模式透传 apiserver 凭证
		if h.proxy {
			if bearer := c.Config.BearerToken; bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("Alertmanager 不可达（%s）: %w", h.label, err)
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("Alertmanager 返回 %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
		}
		return raw, nil
	}
	return nil, lastErr
}

// AMStatus 连通性测试（GET /api/v2/status）
func AMStatus(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig) error {
	_, err := amRequest(ctx, c, cfg, http.MethodGet, "/api/v2/status", nil)
	return err
}

// AMAlerts 拉取当前全部活动告警（含被静默/抑制的，firing 与恢复中的都在内）
func AMAlerts(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig) ([]AMAlert, error) {
	raw, err := amRequest(ctx, c, cfg, http.MethodGet, "/api/v2/alerts?active=true&silenced=true&inhibited=true", nil)
	if err != nil {
		return nil, err
	}
	var out []AMAlert
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 Alertmanager 告警失败: %w", err)
	}
	return out, nil
}

// AMSilences 列出静默规则
func AMSilences(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig) ([]AMSilence, error) {
	raw, err := amRequest(ctx, c, cfg, http.MethodGet, "/api/v2/silences", nil)
	if err != nil {
		return nil, err
	}
	var out []AMSilence
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析静默列表失败: %w", err)
	}
	return out, nil
}

// AMCreateSilence 创建静默（body 为 POST /api/v2/silences 的 JSON）
func AMCreateSilence(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig, body []byte) error {
	_, err := amRequest(ctx, c, cfg, http.MethodPost, "/api/v2/silences", body)
	return err
}

// AMDeleteSilence 删除（过期）静默
func AMDeleteSilence(ctx context.Context, c *kube.Client, cfg *model.AlertmanagerConfig, id string) error {
	_, err := amRequest(ctx, c, cfg, http.MethodDelete, "/api/v2/silence/"+strings.TrimLeft(id, "/"), nil)
	return err
}

// ------------------- AM 主配置 YAML 读写（Secret） -------------------

// AMConfigYAML 集群 AM 主配置内容
type AMConfigYAML struct {
	Secret string `json:"secret"` // "ns/name"
	Key    string `json:"key"`    // 实际使用的 data key（alertmanager.yaml | alertmanager.yaml.gz）
	Gzip   bool   `json:"gzip"`   // 是否为 gzip 存储（operator generated secret）
	YAML   string `json:"yaml"`
}

const amConfigKeyPlain = "alertmanager.yaml"
const amConfigKeyGzip = "alertmanager.yaml.gz"

// splitSecretRef "ns/name" -> (ns, name, error)；name 不得再含斜杠
func splitSecretRef(ref string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(ref), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("Secret 引用格式应为 ns/name，当前 %q", ref)
	}
	if strings.Contains(parts[1], "/") {
		return "", "", fmt.Errorf("Secret 引用格式应为 ns/name，当前 %q", ref)
	}
	return parts[0], parts[1], nil
}

// validateYAML 校验 YAML 语法（能解析为任意合法结构即通过）
func validateYAML(text string) error {
	var out map[string]interface{}
	return yaml.Unmarshal([]byte(text), &out)
}

// amConfigDecode 从 Secret data 提取配置明文（自动识别 gzip / 明文两种 key）
func amConfigDecode(data map[string][]byte) (yamlText, key string, err error) {
	if gz, ok := data[amConfigKeyGzip]; ok {
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return "", "", fmt.Errorf("解压 %s 失败: %w", amConfigKeyGzip, err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return "", "", err
		}
		return string(raw), amConfigKeyGzip, nil
	}
	if raw, ok := data[amConfigKeyPlain]; ok {
		return string(raw), amConfigKeyPlain, nil
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return "", "", fmt.Errorf("Secret 中不含 %s / %s（现有 key: %v）", amConfigKeyPlain, amConfigKeyGzip, keys)
}

// amConfigEncode 保持原 key 与 gzip 格式把配置写回 data（原 Secret 无标准 key 时按明文补齐）
func amConfigEncode(data map[string][]byte, yamlText string) error {
	switch {
	case data[amConfigKeyGzip] != nil:
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write([]byte(yamlText)); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		data[amConfigKeyGzip] = buf.Bytes()
	case data[amConfigKeyPlain] != nil:
		data[amConfigKeyPlain] = []byte(yamlText)
	default:
		data[amConfigKeyPlain] = []byte(yamlText)
	}
	return nil
}

// LoadAMConfigYAML 读取 AM 主配置（自动识别明文与 gzip 两种存储格式）
func LoadAMConfigYAML(ctx context.Context, c *kube.Client, ref string) (*AMConfigYAML, error) {
	ns, name, err := splitSecretRef(ref)
	if err != nil {
		return nil, err
	}
	secret, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("读取 Secret %s 失败: %w", ref, err)
	}
	text, key, err := amConfigDecode(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("Secret %s: %w", ref, err)
	}
	return &AMConfigYAML{Secret: ref, Key: key, Gzip: key == amConfigKeyGzip, YAML: text}, nil
}

// SaveAMConfigYAML 写回 AM 主配置（保持原 key 与 gzip 格式；保存前做 YAML 语法校验）
func SaveAMConfigYAML(ctx context.Context, c *kube.Client, ref, yamlText string) error {
	if err := validateYAML(yamlText); err != nil {
		return fmt.Errorf("YAML 语法校验失败: %w", err)
	}
	ns, name, err := splitSecretRef(ref)
	if err != nil {
		return err
	}
	// Get→Update 无乐观锁保护：与 AM operator 写同一 Secret 并发（或两人同时保存）
	// 会 409——重试（重新 Get 最新 resourceVersion 再编码写回）
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		secret, err := c.Clientset.CoreV1().Secrets(ns).Get(ctx, name, v1.GetOptions{})
		if err != nil {
			return fmt.Errorf("读取 Secret %s 失败: %w", ref, err)
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		if err := amConfigEncode(secret.Data, yamlText); err != nil {
			return err
		}
		if _, err := c.Clientset.CoreV1().Secrets(ns).Update(ctx, secret, v1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				lastErr = err
				continue
			}
			return fmt.Errorf("写回 Secret %s 失败: %w", ref, err)
		}
		return nil
	}
	return fmt.Errorf("写回 Secret %s 冲突重试 3 次仍失败: %w", ref, lastErr)
}
