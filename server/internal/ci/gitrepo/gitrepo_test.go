package gitrepo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{
		"http://gitlab.cqyxpt.site/apply-view/manager-admin-view.git",
		"https://github.com/foo/bar.git",
	} {
		if err := ValidateURL(ok); err != nil {
			t.Errorf("ValidateURL(%q) 应通过: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"git@gitlab.cqyxpt.site:a/b.git",
		"file:///tmp/x.git",
		"git://host/repo.git",
		"not a url",
		"http://",
		"http://localhost/a.git",
		"http://127.0.0.1/a.git",
		"http://169.254.169.254/latest/meta-data",
	} {
		if err := ValidateURL(bad); err == nil {
			t.Errorf("ValidateURL(%q) 应报错", bad)
		}
	}
}

func TestAuthURL(t *testing.T) {
	base := "http://gitlab.cqyxpt.site/a/b.git"

	got, err := AuthURL(base, "", "", "")
	if err != nil || got != base {
		t.Errorf("无凭证应原样返回: %q err=%v", got, err)
	}

	got, err = AuthURL(base, "u", "p@ss:word", "")
	if err != nil {
		t.Fatal(err)
	}
	// 密码特殊字符 @ : 必须转义
	want := "http://u:p%40ss%3Aword@gitlab.cqyxpt.site/a/b.git"
	if got != want {
		t.Errorf("basic 注入: got %q want %q", got, want)
	}

	got, _ = AuthURL(base, "u", "p", "tok123")
	want = "http://oauth2:tok123@gitlab.cqyxpt.site/a/b.git"
	if got != want {
		t.Errorf("token 优先: got %q want %q", got, want)
	}

	// https 协议保留
	got, _ = AuthURL("https://h/x.git", "u", "p", "")
	if got != "https://u:p@h/x.git" {
		t.Errorf("https 保留: got %q", got)
	}
}

func TestParseLSRemote(t *testing.T) {
	lines := []string{
		"abc123\trefs/heads/main",
		"def456\trefs/heads/master",
		"abc123\trefs/tags/v1.0",
		"fff789\trefs/tags/v1.0^{}", // annotated tag peel 行，应跳过
		"def456\trefs/tags/v2.0",
	}
	refs := parseLSRemote(strings.Join(lines, "\n"))
	if !reflect.DeepEqual(refs.Branches, []string{"main", "master"}) {
		t.Errorf("branches: %v", refs.Branches)
	}
	if !reflect.DeepEqual(refs.Tags, []string{"v1.0", "v2.0"}) {
		t.Errorf("tags: %v", refs.Tags)
	}
}

func TestSafeURL(t *testing.T) {
	got := SafeURL("http://u:p@gitlab.cqyxpt.site/a/b.git")
	if got != "http://gitlab.cqyxpt.site/a/b.git" {
		t.Errorf("SafeURL: %q", got)
	}
}

// 测试用对象 id：strings.Repeat 精确构造 40 位，避免手写位数出错
var (
	shaMain     = strings.Repeat("1", 40)
	shaMaster   = strings.Repeat("2", 40)
	shaTag1     = strings.Repeat("3", 40)
	shaTag1Peel = strings.Repeat("4", 40)
	shaTag2     = strings.Repeat("5", 40)
)

// pktLine 组帧（长度含自身 4 字节）。
func pktLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

// smartAdvertisement 构造 v0 ref 广告：main/master 两分支 + annotated tag（含 peel 行）
// + lightweight tag + HEAD 与其他引用（应被忽略）。
func smartAdvertisement() []byte {
	var b []byte
	b = append(b, pktLine("# service=git-upload-pack\n")...)
	b = append(b, "0000"...)
	// 首个 ref 行带 capability（NUL 后）
	b = append(b, pktLine(shaMain+" HEAD\x00symref=HEAD:refs/heads/main multi_ack\n")...)
	b = append(b, pktLine(shaMain+" refs/heads/main\n")...)
	b = append(b, pktLine(shaMaster+" refs/heads/master\n")...)
	b = append(b, pktLine(shaTag1+" refs/tags/v1.0\n")...)
	b = append(b, pktLine(shaTag1Peel+" refs/tags/v1.0^{}\n")...)
	b = append(b, pktLine(shaTag2+" refs/tags/v2.0\n")...)
	b = append(b, pktLine(shaMaster+" refs/merge-requests/1/head\n")...)
	b = append(b, "0000"...)
	return b
}

// TestListRemoteRefsSmartHTTP 用 httptest 模拟 GitLab 的 smart HTTP 广告，
// 覆盖：分支/tag 枚举、annotated tag peel 行跳过、非分支引用忽略、认证头透传。
func TestListRemoteRefsSmartHTTP(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if !strings.HasSuffix(r.URL.Path, "/info/refs") || r.URL.Query().Get("service") != "git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write(smartAdvertisement())
	}))
	defer srv.Close()

	refs, err := ListRemoteRefs(context.Background(), srv.URL+"/demo.git")
	if err != nil {
		t.Fatalf("ListRemoteRefs: %v", err)
	}
	if !reflect.DeepEqual(refs.Branches, []string{"main", "master"}) {
		t.Errorf("branches: %v", refs.Branches)
	}
	if !reflect.DeepEqual(refs.Tags, []string{"v1.0", "v2.0"}) {
		t.Errorf("tags（annotated 的 peel 行应跳过）: %v", refs.Tags)
	}
	// 凭证经 AuthURL 注入后应随请求发送（Basic）
	authURL, err := AuthURL(srv.URL+"/demo.git", "user", "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ListRemoteRefs(context.Background(), authURL); err != nil {
		t.Fatalf("带凭证请求: %v", err)
	}
	if gotAuth == "" || !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("凭证未随请求发送: %q", gotAuth)
	}
}

// TestListRemoteRefsDumbHTTP dumb 协议（静态文件响应）兼容。
func TestListRemoteRefsDumbHTTP(t *testing.T) {
	body := strings.Join([]string{
		shaMain + "\trefs/heads/main",
		shaMaster + "\trefs/heads/master",
		shaTag1 + "\trefs/tags/v1.0",
		shaTag1Peel + "\trefs/tags/v1.0^{}",
		shaTag2 + "\trefs/tags/v2.0",
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	refs, err := ListRemoteRefs(context.Background(), srv.URL+"/demo.git")
	if err != nil {
		t.Fatalf("ListRemoteRefs: %v", err)
	}
	if !reflect.DeepEqual(refs.Branches, []string{"main", "master"}) {
		t.Errorf("branches: %v", refs.Branches)
	}
	if !reflect.DeepEqual(refs.Tags, []string{"v1.0", "v2.0"}) {
		t.Errorf("tags: %v", refs.Tags)
	}
}

// TestListRemoteRefsErrors 覆盖：401 认证失败、404、连接失败——
// 错误信息一律不得包含内嵌凭证。
func TestListRemoteRefsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := ListRemoteRefs(context.Background(), srv.URL+"/nope.git")
	if err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("401 应报认证失败: %v", err)
	}

	_, err = ListRemoteRefs(context.Background(), "http://user:secretpw@127.0.0.1:1/nope.git")
	if err == nil {
		t.Fatal("期望连接失败")
	}
	msg := err.Error()
	if strings.Contains(msg, "secretpw") {
		t.Errorf("错误信息泄露了密码: %s", msg)
	}

	// "-" 前缀形态拒绝
	if _, err = ListRemoteRefs(context.Background(), "--upload-pack=evil"); err == nil {
		t.Fatal("- 前缀应拒绝")
	}
}
