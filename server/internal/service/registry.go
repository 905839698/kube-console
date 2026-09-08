// Harbor v2 API 只读客户端（项目/仓库/制品浏览）
package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RegistryClient Harbor v2 API 客户端
type RegistryClient struct {
	BaseURL  string
	Username string
	Password string
	Insecure bool
	hc       *http.Client
}

// NewRegistryClient 构建客户端
func NewRegistryClient(baseURL, username, password string, insecure bool) (*RegistryClient, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http") {
		baseURL = "https://" + baseURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("仓库地址无效: %w", err)
	}
	return &RegistryClient{
		BaseURL: baseURL, Username: username, Password: password, Insecure: insecure,
		hc: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
			},
		},
	}, nil
}

// doRequest 底层请求：返回原始状态码与 body（供扫描触发/报告下载等非标准 JSON 场景）
func (r *RegistryClient) doRequest(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	u := r.BaseURL + path
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, err
	}
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("镜像仓库不可达: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, b, err
}

func (r *RegistryClient) get(ctx context.Context, path string, out any) error {
	u := r.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("镜像仓库不可达: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if isHTMLBody(resp.Header.Get("Content-Type"), body) {
		return htmlResponseErr
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("认证失败（检查用户名/密码）")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("仓库返回 %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("响应解析失败: %w", err)
	}
	return nil
}

// isHTMLBody 判断响应是否为 HTML 页面（Harbor 网关对未知 API 路径会回退到前端页面）
func isHTMLBody(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	t := strings.TrimSpace(string(body[:minInt(len(body), 64)]))
	return strings.HasPrefix(t, "<") || strings.HasPrefix(t, "<!DOCTYPE")
}

// htmlResponseErr 网关回退到 HTML 页面时的明确提示
var htmlResponseErr = fmt.Errorf("仓库返回了 HTML 页面：URL 配置有误（应填 Harbor 根地址，如 https://harbor.example.com:8443，不要带 /api 或路径后缀）")

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RegProject Harbor 项目
type RegProject struct {
	Name      string `json:"name"`
	RepoCount int    `json:"repo_count"`
}

// RegRepository Harbor 仓库（项目内镜像）
type RegRepository struct {
	Name        string `json:"name"` // project/repo
	ArtifactCount int  `json:"artifact_count"`
	PullCount   int64  `json:"pull_count"`
	UpdateTime  string `json:"update_time"`
}

// RegArtifact 制品（tag 列表内嵌）
type RegArtifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	PushTime  string `json:"push_time"`
	PullTime  string `json:"pull_time"`
	Tags      []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

// ListProjects 项目列表
func (r *RegistryClient) ListProjects(ctx context.Context, search string) ([]RegProject, error) {
	var projects []RegProject
	q := "/api/v2.0/projects?page=1&page_size=100"
	if search != "" {
		q += "&name=" + url.QueryEscape(search)
	}
	if err := r.get(ctx, q, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// ListRepositories 项目内仓库列表
func (r *RegistryClient) ListRepositories(ctx context.Context, project, search string) ([]RegRepository, error) {
	var repos []RegRepository
	q := fmt.Sprintf("/api/v2.0/projects/%s/repositories?page=1&page_size=100", url.PathEscape(project))
	if search != "" {
		q += "&q=name=~" + url.QueryEscape(search)
	}
	if err := r.get(ctx, q, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// ListArtifacts 镜像 tag 列表
func (r *RegistryClient) ListArtifacts(ctx context.Context, project, repo string) ([]RegArtifact, error) {
	var artifacts []RegArtifact
	q := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts?page=1&page_size=50&with_tag=true",
		url.PathEscape(project), url.PathEscape(repo))
	if err := r.get(ctx, q, &artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

// ------------------- 镜像扫描与漏洞报告（Harbor 内置 Trivy） -------------------

// ScanOverview 制品扫描状态与汇总
type ScanOverview struct {
	ScanStatus string           `json:"scanStatus"` // NotScanned/Pending/Running/Success/Error
	Severity   string           `json:"severity"`   // 最高级别
	Digest     string           `json:"digest"`
	PushTime   string           `json:"pushTime"`
	Counts     map[string]int64 `json:"counts"` // Critical/High/Medium/Low/Unknown/Negligible
}

// vulnSeverityRank 数字越大越严重
func vulnSeverityRank(s string) int {
	switch strings.ToUpper(s) {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	}
	return 0
}

// TriggerScan 触发制品扫描（已在扫描中返回 409，视为成功）
func (r *RegistryClient) TriggerScan(ctx context.Context, project, repo, reference string) error {
	code, body, err := r.doRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts/%s/scan",
			url.PathEscape(project), url.PathEscape(repo), url.PathEscape(reference)), nil)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK, http.StatusAccepted, http.StatusCreated, http.StatusConflict:
		return nil
	}
	return fmt.Errorf("触发扫描返回 %d: %s", code, truncateStr(string(body), 200))
}

// GetArtifactScan 查询制品扫描状态与漏洞计数
func (r *RegistryClient) GetArtifactScan(ctx context.Context, project, repo, reference string) (*ScanOverview, error) {
	var raw map[string]any
	if _, body, err := r.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts/%s?with_scan_overview=true",
			url.PathEscape(project), url.PathEscape(repo), url.PathEscape(reference)), nil); err != nil {
		return nil, err
	} else if isHTMLBody("", body) {
		return nil, htmlResponseErr
	} else if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	out := &ScanOverview{Counts: map[string]int64{}}
	out.Digest, _ = raw["digest"].(string)
	if pt, ok := raw["push_time"].(string); ok {
		out.PushTime = pt
	}
	if so, ok := raw["scan_overview"].(map[string]any); ok {
		// mime-type 键唯一（application/vnd.scanner.adapter.vuln.report.harbor）
		for _, v := range so {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			out.ScanStatus, _ = m["scan_status"].(string)
			out.Severity, _ = m["severity"].(string)
			if summary, ok := m["summary"].(map[string]any); ok {
				if inner, ok := summary["summary"].(map[string]any); ok {
					for sev, n := range inner {
						if f, ok := n.(float64); ok {
							out.Counts[sev] = int64(f)
						}
					}
				}
			}
		}
	}
	return out, nil
}

// Vulnerability 一条 CVE
type Vulnerability struct {
	ID               string  `json:"id"`
	Severity         string  `json:"severity"`
	PkgName          string  `json:"pkgName"`
	InstalledVersion string  `json:"installedVersion"`
	FixedVersion     string  `json:"fixedVersion"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	PrimaryURL       string  `json:"primaryURL"`
	CVSSScore        float64 `json:"cvssScore,omitempty"`
	Status           string  `json:"status,omitempty"` // fixed/wontfix/unknown（1.1 报告）
}

// VulnReport 漏洞报告（Trivy）
type VulnReport struct {
	GeneratedAt string          `json:"generatedAt"`
	Scanner     string          `json:"scanner"`
	Severity    string          `json:"severity"`
	Count       int             `json:"count"`
	Items       []Vulnerability `json:"items"`
}

// GetVulnerabilities 拉取制品 CVE 明细（Trivy 报告）
func (r *RegistryClient) GetVulnerabilities(ctx context.Context, project, repo, reference string) (*VulnReport, error) {
	_, body, err := r.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts/%s/additions/vulnerabilities",
			url.PathEscape(project), url.PathEscape(repo), url.PathEscape(reference)), nil)
	if err != nil {
		return nil, err
	}
	if isHTMLBody("", body) {
		return nil, htmlResponseErr
	}
	out, err := parseVulnReport(body)
	if err != nil {
		return nil, fmt.Errorf("漏洞报告解析失败: %w", err)
	}
	return out, nil
}

// parseVulnReport 解析漏洞报告，兼容两种格式：
//   - Harbor 3.x（Trivy 扫描器适配器）：整包用 content-type 键包裹
//     （application/vnd.security.vulnerability.report; version=1.1），字段小写
//     generated_at/scanner{name,version}/severity/vulnerabilities[{id,package,version,
//     fix_version,severity,status,description,links,preferred_cvss}]
//   - Harbor 2.x 旧格式：扁平 PascalCase（GeneratedAt/Scanner{Name,Version}/
//     Severity/Vulnerabilities[{ID,PkgName,...}]）
func parseVulnReport(body []byte) (*VulnReport, error) {
	var outer map[string]any
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, err
	}
	inner := outer
	if len(outer) == 1 {
		for k, v := range outer {
			if strings.HasPrefix(k, "application/vnd.security.vulnerability.report") {
				if m, ok := v.(map[string]any); ok {
					inner = m
				}
			}
		}
	}
	out := &VulnReport{Items: []Vulnerability{}}
	if v, ok := inner["generated_at"].(string); ok {
		out.GeneratedAt = v
	} else if v, ok := inner["GeneratedAt"].(string); ok {
		out.GeneratedAt = v
	}
	if sc, ok := inner["scanner"].(map[string]any); ok {
		out.Scanner, _ = sc["name"].(string)
		if ver, ok := sc["version"].(string); ok {
			out.Scanner += " " + ver
		}
	} else if sc, ok := inner["Scanner"].(map[string]any); ok {
		out.Scanner, _ = sc["Name"].(string)
		if ver, ok := sc["Version"].(string); ok {
			out.Scanner += " " + ver
		}
	}
	if v, ok := inner["severity"].(string); ok {
		out.Severity = v
	} else if v, ok := inner["Severity"].(string); ok {
		out.Severity = v
	}

	if list, ok := inner["vulnerabilities"].([]any); ok {
		for _, rv := range list {
			m, ok := rv.(map[string]any)
			if !ok {
				continue
			}
			v := Vulnerability{}
			v.ID, _ = m["id"].(string)
			v.Severity, _ = m["severity"].(string)
			v.PkgName, _ = m["package"].(string)
			v.InstalledVersion, _ = m["version"].(string)
			v.FixedVersion, _ = m["fix_version"].(string)
			v.Description, _ = m["description"].(string)
			v.Status, _ = m["status"].(string)
			// links 元素可能是纯字符串（Harbor 3.x Trivy 输出）或 {url,type} 对象
			if links, ok := m["links"].([]any); ok {
				for _, rl := range links {
					switch u := rl.(type) {
					case string:
						if u != "" {
							v.PrimaryURL = u
						}
					case map[string]any:
						ltype, _ := u["type"].(string)
						if s, ok := u["url"].(string); ok && (ltype == "ADVISORY" || v.PrimaryURL == "") {
							v.PrimaryURL = s
						}
					}
					if v.PrimaryURL != "" {
						break
					}
				}
			}
			if cv, ok := m["preferred_cvss"].(map[string]any); ok {
				v.CVSSScore, _ = cv["score_v3"].(float64)
			}
			out.Items = append(out.Items, v)
		}
	} else if list, ok := inner["Vulnerabilities"].([]any); ok {
		for _, rv := range list {
			m, ok := rv.(map[string]any)
			if !ok {
				continue
			}
			v := Vulnerability{}
			v.ID, _ = m["ID"].(string)
			v.Severity, _ = m["Severity"].(string)
			v.PkgName, _ = m["PkgName"].(string)
			v.InstalledVersion, _ = m["InstalledVersion"].(string)
			v.FixedVersion, _ = m["FixedVersion"].(string)
			v.Title, _ = m["Title"].(string)
			v.Description, _ = m["Description"].(string)
			v.PrimaryURL, _ = m["PrimaryURL"].(string)
			out.Items = append(out.Items, v)
		}
	}

	out.Count = len(out.Items)
	sortVulns(out.Items)
	return out, nil
}

// sortVulns 按严重度降序
func sortVulns(items []Vulnerability) {
	// 简单插入排序（报告规模可控）
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && vulnSeverityRank(items[j].Severity) > vulnSeverityRank(items[j-1].Severity); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// ParseImageRef 拆解镜像引用：host/project/repo[:tag|@digest]
func ParseImageRef(image string) (host, project, repo, reference string, err error) {
	image = strings.TrimSpace(image)
	if image == "" || !strings.Contains(image, "/") {
		return "", "", "", "", fmt.Errorf("镜像引用无效（应为 registry/project/app:tag）")
	}
	rest := image
	if i := strings.Index(rest, "/"); i >= 0 {
		host = rest[:i]
		rest = rest[i+1:]
	}
	if !strings.Contains(rest, "/") {
		return "", "", "", "", fmt.Errorf("镜像引用缺少 project/repo 段")
	}
	reference = "latest"
	if i := strings.Index(rest, "@"); i >= 0 {
		reference = rest[i+1:]
		rest = rest[:i]
	} else if i := strings.LastIndex(rest, ":"); i >= 0 {
		reference = rest[i+1:]
		rest = rest[:i]
	}
	parts := strings.SplitN(rest, "/", 2)
	project, repo = parts[0], parts[1]
	return host, project, repo, reference, nil
}

// ImageTags 按完整镜像引用拉取可选 Tag 列表（校验镜像属于已配置仓库）
func (r *RegistryClient) ImageTags(ctx context.Context, image string) ([]string, error) {
	host, project, repo, _, err := ParseImageRef(image)
	if err != nil {
		return nil, err
	}
	if cfgHost := r.host(); host != cfgHost {
		return nil, fmt.Errorf("镜像 registry（%s）与已配置仓库（%s）不一致", host, cfgHost)
	}
	var artifacts []RegArtifact
	if _, body, err := r.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts?with_tag=true&page_size=100",
			url.PathEscape(project), url.PathEscape(repo)), nil); err != nil {
		return nil, err
	} else if isHTMLBody("", body) {
		return nil, htmlResponseErr
	} else if err := json.Unmarshal(body, &artifacts); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	tags := []string{}
	for _, a := range artifacts {
		for _, t := range a.Tags {
			tags = append(tags, t.Name)
		}
	}
	return tags, nil
}

// ImageVulns 按完整镜像引用取扫描状态 + CVE 报告（未完成扫描时 report 为空）
func (r *RegistryClient) ImageVulns(ctx context.Context, image string) (*ScanOverview, *VulnReport, error) {
	host, project, repo, reference, err := ParseImageRef(image)
	if err != nil {
		return nil, nil, err
	}
	if cfgHost := r.host(); host != cfgHost {
		return nil, nil, fmt.Errorf("镜像 registry（%s）与已配置仓库（%s）不一致", host, cfgHost)
	}
	ov, err := r.GetArtifactScan(ctx, project, repo, reference)
	if err != nil {
		return nil, nil, err
	}
	if ov.ScanStatus != "Success" {
		return ov, nil, nil
	}
	report, err := r.GetVulnerabilities(ctx, project, repo, reference)
	if err != nil {
		return ov, nil, nil
	}
	return ov, report, nil
}

// host 配置仓库的 host:port（去 scheme）
func (r *RegistryClient) host() string {
	return strings.TrimPrefix(strings.TrimPrefix(r.BaseURL, "https://"), "http://")
}
