// Package gitrepo 查询 Git 仓库的分支/tag 列表（设计器 git-clone 节点下拉选项）。
//
// 实现方式：直接说 Git smart HTTP 协议——GET {repo}/info/refs?service=git-upload-pack，
// 解析 pkt-line ref 广告（兼容 dumb HTTP 静态文件的旧协议响应）。
// 不 fork git 进程：外部输入（仓库地址）不进入任何进程执行接口，
// 服务镜像无需 git 二进制，也无子进程超时/回收问题。
// 协议定位是"GitLab/GitHub/Gitee 通用"，与流水线 git-clone 的认证方式一致。
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kube-console/server/internal/ci/errcode"
)

// Refs 仓库的分支与 tag 列表。
type Refs struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
}

// MaxRefs 单类 ref 返回上限（超大仓库防响应膨胀，超出截断）。
const MaxRefs = 500

// lsRemoteTimeout 单次 info/refs 请求超时。
const lsRemoteTimeout = 20 * time.Second

// maxAdvertisementBytes ref 广告响应体上限（超大仓库防护）。
const maxAdvertisementBytes = 16 << 20

// checkResolvedIP 解析后的 IP 复检：环回/链路本地/未指定一律拒绝（私网段
// 10/172.16/192.168 是内网 git 服务的常见落点，不拦）。包级变量便于测试绕过
// 环回拦截（httptest 服务器监听在 127.0.0.1）。
var checkResolvedIP = func(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// safeDialContext 对解析出的每个 IP 复检后再建连。
// ValidateURL 只比对字面量，`127.0.0.1.xip.io`、`localhost.`（尾点）、DNS rebinding
// 这类能绕过字面量检查；解析后复检才能堵住。TLS 的 ServerName 仍取自 URL host
// （DialContext 只换拨号地址，不影响握手），私网 git 服务不受影响。
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, rerr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if rerr != nil {
			return nil, rerr
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if checkResolvedIP(ip) {
			return nil, &net.DNSError{Err: "禁止访问的地址", Name: host, IsTemporary: false}
		}
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "无可用地址", Name: host, IsTemporary: false}
	}
	var d net.Dialer
	d.Timeout = 10 * time.Second
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// lsRemoteHTTP 复用的 HTTP 客户端。
var lsRemoteHTTP = &http.Client{
	Timeout: lsRemoteTimeout,
	Transport: &http.Transport{
		DialContext:           safeDialContext,
		ResponseHeaderTimeout: 15 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("仓库重定向过多（>3）")
		}
		// 重定向目标必须重跑校验：攻击者控制（或已被攻陷）的 git host 可以
		// 302 到元数据服务/内网，且解析失败只会得到「空 ref 列表」不会被拦
		if err := ValidateURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	},
}

// ValidateURL 只接受 http(s) 仓库地址。
// 拒绝 ssh/file/git 等协议：凭证注入逻辑只覆盖 http(s)，且本实现只会发起 HTTP 请求。
// 同时拒绝环回/链路本地/云元数据目标：server 代为发起请求，放行这些地址等于
// 把 server 当 SSRF 探针（错误信息还会回显失败详情）。
// 内网 git 服务常用的私网段（10/172.16/192.168）不受影响。
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errcode.Newf(errcode.InvalidParam, "非法仓库地址: %s", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errcode.Newf(errcode.InvalidParam, "仅支持 http/https 仓库地址: %s", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" {
		return errcode.Newf(errcode.InvalidParam, "禁止访问该地址: %s", SafeURL(raw))
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return errcode.Newf(errcode.InvalidParam, "禁止访问该地址: %s", SafeURL(raw))
		}
	}
	return nil
}

// AuthURL 把凭证注入仓库地址（保留原协议），注入规则与 nodes/git-clone/task.yaml
// 一致：token 优先（oauth2:token@），否则 username:password@。凭证为空时原样返回。
// 用 net/url 构造 userinfo，密码中的 @ : 等特殊字符会被正确转义。
func AuthURL(raw, username, password, token string) (string, error) {
	if token == "" && username == "" && password == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errcode.Newf(errcode.InvalidParam, "非法仓库地址: %s", raw)
	}
	switch {
	case token != "":
		u.User = url.UserPassword("oauth2", token)
	default:
		u.User = url.UserPassword(username, password)
	}
	return u.String(), nil
}

// ListRemoteRefs 枚举仓库的分支/tag（Git smart HTTP：info/refs?service=git-upload-pack）。
// repoURL 可内嵌凭证（AuthURL 构造：token 走 oauth2:token@，或 basic）。
// 兼容 smart（pkt-line 广告）与 dumb（静态 "sha<TAB>ref" 文件）两种响应。
// 错误信息中的 URL 已剥离凭证，避免泄露。
func ListRemoteRefs(ctx context.Context, repoURL string) (*Refs, error) {
	trimmed := strings.TrimSpace(repoURL)
	// "-" 前缀形态一律拒绝（保持输入约定清晰，防各调用方传入异常值）
	if trimmed == "" || strings.HasPrefix(trimmed, "-") {
		return nil, errcode.Newf(errcode.InvalidParam, "非法仓库地址: %s", SafeURL(repoURL))
	}
	base := strings.TrimSuffix(trimmed, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, errcode.Newf(errcode.InvalidParam, "非法仓库地址: %s", SafeURL(repoURL))
	}
	// 不发送 Git-Protocol: version=2 → 服务端按经典 v0 协议返回 ref 广告
	resp, err := lsRemoteHTTP.Do(req)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "连接仓库失败: %v", sanitizeHTTPErr(err))
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errcode.Newf(errcode.DepUnavailable, "仓库认证失败（HTTP %d）: %s",
			resp.StatusCode, SafeURL(repoURL))
	case http.StatusNotFound:
		return nil, errcode.Newf(errcode.DepUnavailable, "仓库不存在（HTTP 404）: %s", SafeURL(repoURL))
	default:
		return nil, errcode.Newf(errcode.DepUnavailable, "仓库响应异常（HTTP %d）: %s",
			resp.StatusCode, SafeURL(repoURL))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAdvertisementBytes))
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "读取仓库响应失败: %v", sanitizeHTTPErr(err))
	}
	return parseRefAdvertisement(body)
}

// parseRefAdvertisement 解析 info/refs 响应：smart（pkt-line）或 dumb（纯文本）。
// smart 响应的广告体里必有 "# service=" 声明；dumb 是 "sha\tref" 静态文本。
func parseRefAdvertisement(body []byte) (*Refs, error) {
	if i := bytes.Index(body, []byte("# service=")); i >= 0 && i < 64 {
		return parseSmartRefs(body)
	}
	return parseLSRemote(string(body)), nil
}

// parseSmartRefs 解析 v0 pkt-line ref 广告：
//
//	<pkt># service=git-upload-pack\n  <flush 0000>
//	<pkt><sha> <ref>\0<capabilities>\n
//	<pkt><sha> <ref>\n ...
//	<flush 0000>
//
// 跳过 annotated tag 的 peel 行（refs/tags/x^{}）；只收集分支与 tag。
func parseSmartRefs(body []byte) (*Refs, error) {
	refs := &Refs{Branches: []string{}, Tags: []string{}}
	seen := map[string]bool{}
	empty := true
	for _, pkt := range pktLines(body) {
		if len(pkt) == 0 {
			continue // flush-pkt
		}
		empty = false
		payload := pkt
		// 首个 ref 行的 capability 列表跟在 NUL 之后，去掉
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		line := strings.TrimRight(string(payload), "\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue // service 声明行
		}
		if strings.HasPrefix(line, "version 2") {
			return nil, errcode.New(errcode.DepUnavailable, "不支持的协议响应（protocol v2）")
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue // shallow 等异常行
		}
		sha, ref := fields[0], fields[1]
		if len(sha) != 40 && len(sha) != 64 {
			continue // 不是对象 id 的行
		}
		if strings.HasSuffix(ref, "^{}") {
			continue // annotated tag peel 行
		}
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			name := strings.TrimPrefix(ref, "refs/heads/")
			if len(refs.Branches) < MaxRefs && !seen["b/"+name] {
				seen["b/"+name] = true
				refs.Branches = append(refs.Branches, name)
			}
		case strings.HasPrefix(ref, "refs/tags/"):
			name := strings.TrimPrefix(ref, "refs/tags/")
			if len(refs.Tags) < MaxRefs && !seen["t/"+name] {
				seen["t/"+name] = true
				refs.Tags = append(refs.Tags, name)
			}
		}
	}
	if empty {
		return nil, errcode.New(errcode.DepUnavailable, "仓库响应不是合法的 ref 广告")
	}
	return refs, nil
}

// pktLines 按 pkt-line 分帧迭代 body（帧长 = 4 位十六进制，含自身 4 字节）。
// "0000"（解析值为 0）是 flush-pkt，产出空切片后继续；0001-0003 为保留帧，
// 出现即视为帧损坏终止。
func pktLines(body []byte) [][]byte {
	var out [][]byte
	for i := 0; i+4 <= len(body); {
		n, ok := parseHex4(body[i : i+4])
		if !ok || i+n > len(body) {
			return out
		}
		switch {
		case n == 0: // flush-pkt
			out = append(out, nil)
			i += 4
		case n < 4:
			return out // 保留帧/损坏帧
		default:
			out = append(out, body[i+4:i+n])
			i += n
		}
	}
	return out
}

// parseHex4 解析 4 位小写/大写十六进制 pkt 长度。
func parseHex4(b []byte) (int, bool) {
	v := 0
	for _, c := range b {
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		v = v*16 + d
	}
	return v, true
}

// parseLSRemote 解析 dumb HTTP 协议的 info/refs 静态文件（"sha\tref" 每行一条）。
// 跳过 annotated tag 的 peel 行（refs/tags/x^{}）。
func parseLSRemote(out string) *Refs {
	refs := &Refs{Branches: []string{}, Tags: []string{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		ref := parts[1]
		if strings.Contains(ref, "^{}") {
			continue
		}
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			name := strings.TrimPrefix(ref, "refs/heads/")
			if len(refs.Branches) < MaxRefs && !seen["b/"+name] {
				seen["b/"+name] = true
				refs.Branches = append(refs.Branches, name)
			}
		case strings.HasPrefix(ref, "refs/tags/"):
			name := strings.TrimPrefix(ref, "refs/tags/")
			if len(refs.Tags) < MaxRefs && !seen["t/"+name] {
				seen["t/"+name] = true
				refs.Tags = append(refs.Tags, name)
			}
		}
	}
	return refs
}

// sanitizeHTTPErr 剥离 net/http 错误中的 URL（可能含内嵌凭证）。
func sanitizeHTTPErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Sprintf("%s %s: %v", ue.Op, SafeURL(ue.URL), ue.Err)
	}
	return err.Error()
}

// SafeURL 剥离 URL 中的 userinfo（日志/错误信息用）。
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	u.User = nil
	return u.String()
}
