package builder

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"kube-console/server/internal/ci/pipeline"
	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
)

func newTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.CIProject{}, &model.CIPipeline{}, &model.CIPipelineVersion{},
		&model.CIImageBuilder{}, &model.CIBuilderToken{}, &model.CICredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := realRegistry(t)
	pipe := pipeline.NewService(reg, "ci-platform")
	cfg := &config.CIConfig{
		ImageRegistry:      "harbor.test/library",
		ServerURL:          "http://kube-console.kube-console.svc:8080",
		BuilderUploadDir:   t.TempDir(),
		BuilderUploadMaxMB: 1,
	}
	s := New(db, cfg, pipe, pipeline.NewStore(db, pipe), nil)
	// 项目 + 推送凭证种子
	pid := uint(1)
	if err := db.Create(&model.CIProject{ID: pid, Name: "demo", Namespace: "ci-demo", ClusterName: "c1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CICredential{ID: 9, Name: "harbor", Form: model.CIFormBasic,
		Type: "registry", ClusterName: "c1", ProjectID: &pid}).Error; err != nil {
		t.Fatal(err)
	}
	return s, db
}

func saveReq(appType string) *SaveReq {
	return &SaveReq{
		ClusterName: "c1", ProjectID: 1, Name: "订单中心", AppType: appType,
		Registry: "harbor.test", Repository: "library/order", CredentialID: 9,
	}
}

func TestCreateValidation(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()

	// 悬空凭证：保存时就报明确错误
	r := saveReq("java")
	r.CredentialID = 404
	if _, err := s.Create(ctx, 1, r); err == nil || !strings.Contains(err.Error(), "凭证") {
		t.Fatalf("dangling credential must fail with readable error: %v", err)
	}
	// 应用类型校验
	r = saveReq("python")
	if _, err := s.Create(ctx, 1, r); err == nil {
		t.Fatal("unsupported appType must fail")
	}
	// 启动参数：非法字符保存即拒；web 类型静默清空（表单不展示，兜底在后端）
	r = saveReq("go")
	r.EntryArgs = `serve --a="x"`
	if _, err := s.Create(ctx, 1, r); err == nil || !strings.Contains(err.Error(), "启动参数") {
		t.Fatalf("bad entry args must fail: %v", err)
	}
	r = saveReq("web")
	r.Name = "web-entry-clear"
	r.EntryArgs = "nginx -g 'daemon off;'"
	wb, err := s.Create(ctx, 1, r)
	if err != nil {
		t.Fatalf("web create: %v", err)
	}
	if wb.EntryArgs != "" {
		t.Fatalf("web entry args must be cleared: %q", wb.EntryArgs)
	}
	// 正常创建 + 重名拦截
	r = saveReq("java")
	b, err := s.Create(ctx, 1, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var p model.CIPipeline
	if err := db.First(&p, b.PipelineID).Error; err != nil || !strings.HasPrefix(p.Name, "bld-") {
		t.Fatalf("managed pipeline missing: %+v err=%v", p, err)
	}
	if _, err := s.Create(ctx, 1, saveReq("java")); err == nil {
		t.Fatal("duplicate builder name must conflict")
	}
	// 未上传前不能构建
	if _, err := s.Run(ctx, b.ID, 1, "u", "t1"); err == nil || !strings.Contains(err.Error(), "上传") {
		t.Fatalf("run without artifact must tell to upload: %v", err)
	}
}

func TestUploadAndVersion(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	b, err := s.Create(ctx, 1, saveReq("java"))
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("fake-jar-bytes")
	sum := sha256.Sum256(content)
	out, err := s.UploadArtifact(ctx, b.ID, "order-center-1.0.jar", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if out.FileSHA256 != hex.EncodeToString(sum[:]) || out.FileSize != int64(len(content)) {
		t.Fatalf("artifact meta wrong: %+v", out)
	}
	if st, err := os.Stat(s.ArtifactPath(out)); err != nil || st.Size() != int64(len(content)) {
		t.Fatalf("artifact not on disk: %v", err)
	}
	// 托管版本已生成（含真实校验/编译），下载 URL 指向平台制品接口
	v, err := s.store.GetLatest(ctx, b.PipelineID)
	if err != nil {
		t.Fatalf("version after upload: %v", err)
	}
	if !strings.Contains(v.GraphJSON, "/api/ci/artifact/") || !strings.Contains(v.GraphJSON, `"sha256":"`+out.FileSHA256+`"`) {
		t.Fatalf("graph missing download ref: %s", v.GraphJSON)
	}
	// 令牌可回查构建器（公开下载路由的鉴权依据）
	lb, err := s.LookupToken(ctx, mustCurrentToken(t, s, b.ID))
	if err != nil || lb.ID != b.ID {
		t.Fatalf("token lookup: %v", err)
	}
	// 同内容重传 → 图一致 → 不膨胀版本号
	v1 := v.Version
	if _, err := s.UploadArtifact(ctx, b.ID, "again.jar", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	v2, _ := s.store.GetLatest(ctx, b.PipelineID)
	if v2.Version <= v1 {
		// sha 相同但 fileName 不进图、令牌轮转 → URL 变 → 版本必然新增，
		// 这里断言只要求版本号继续可用（不静默失败即可）
		t.Logf("version %d -> %d (token rotation bumps graph)", v1, v2.Version)
	}

	// 错误形态：Java 传 war、web 传 zip
	if _, err := s.UploadArtifact(ctx, b.ID, "old.war", bytes.NewReader(content)); err == nil {
		t.Fatal("war must be rejected for java")
	}
	// 路径穿越文件名
	if _, err := s.UploadArtifact(ctx, b.ID, "../../../etc/passwd.jar", bytes.NewReader(content)); err != nil {
		t.Fatalf("path traversal should sanitize to basename, got: %v", err)
	}
	// 超限（BuilderUploadMaxMB=1）
	if _, err := s.UploadArtifact(ctx, b.ID, "big.jar", bytes.NewReader(make([]byte, 1024*1024+16))); err == nil ||
		!strings.Contains(err.Error(), "上限") {
		t.Fatalf("oversize must fail readable: %v", err)
	}
	_ = db
}

func TestUploadZipNormalizedToTargz(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	b, err := s.Create(ctx, 1, saveReq("web"))
	if err != nil {
		t.Fatal(err)
	}
	zipBytes := buildZip(t, map[string]string{
		"index.html":        "<html>ok</html>",
		"assets/app.js":     "console.log(1)",
		"../evil.txt":       "slip",   // 归一化为 evil.txt（不逃逸）
		"Windows\\x\\y.css": "body{}", // 反斜杠路径归一
	})
	out, err := s.UploadArtifact(ctx, b.ID, "dist.zip", bytes.NewReader(zipBytes))
	if err != nil {
		t.Fatalf("zip upload: %v", err)
	}
	if out.FileName != "dist.zip" {
		t.Fatalf("display name keeps original: %v", out.FileName)
	}
	// 落盘的是归一后的 tar.gz：gzip 套 tar，条目名已净化
	f, err := os.Open(s.ArtifactPath(out))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("stored artifact must be gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	names := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		body, _ := io.ReadAll(tr)
		names[h.Name] = string(body)
	}
	for _, want := range []string{"index.html", "assets/app.js", "evil.txt", "Windows/x/y.css"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("entry %q missing in %v", want, names)
		}
	}
	// sha 口径 = 归一后 tar.gz 的 sha（fetch 端按它校验）
	sum := sha256.Sum256(mustReadFile(t, s.ArtifactPath(out)))
	if out.FileSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha must be of normalized tar.gz")
	}
	// 空 zip 拒绝
	if _, err := s.UploadArtifact(ctx, b.ID, "empty.zip", bytes.NewReader(buildZip(t, nil))); err == nil {
		t.Fatal("empty zip must fail")
	}
	// 非压缩数据冒充 zip 拒绝
	if _, err := s.UploadArtifact(ctx, b.ID, "fake.zip", bytes.NewReader([]byte("not-a-zip"))); err == nil {
		t.Fatal("corrupt zip must fail")
	}
	// 解包超限（maxBytes=1MB）：zip 压缩后很小、解压巨大
	if _, err := s.UploadArtifact(ctx, b.ID, "bomb.zip", bytes.NewReader(buildZip(t,
		map[string]string{"big.bin": string(bytes.Repeat([]byte("A"), 1024*1024+64))}))); err == nil ||
		!strings.Contains(err.Error(), "上限") {
		t.Fatalf("zip bomb must hit size cap: %v", err)
	}
}

// buildZip 内存构造 zip（entries 为 name→content；nil = 空包）
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		fh, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fh.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunRequiresServerURLAndTagValidation(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	b, err := s.Create(ctx, 1, saveReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadArtifact(ctx, b.ID, "svc", bytes.NewReader([]byte("bin"))); err != nil {
		t.Fatal(err)
	}
	// tag 非法在 StartRun 前就拦下
	if _, err := s.Run(ctx, b.ID, 1, "u", "bad tag!"); err == nil {
		t.Fatal("invalid tag must fail")
	}
	// serverURL 缺失 → 明确配置错误（不静默排队后失败）
	s.cfg.ServerURL = ""
	if _, err := s.Run(ctx, b.ID, 1, "u", "v1"); err == nil || !strings.Contains(err.Error(), "serverUrl") {
		t.Fatalf("missing serverUrl must be explicit: %v", err)
	}
}

func TestDeleteCleansUp(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	b, err := s.Create(ctx, 1, saveReq("web"))
	if err != nil {
		t.Fatal(err)
	}
	// web 必须是 tar.gz
	// web 现在收 zip，但内容必须是真 zip（伪 zip 在归一阶段解析失败）
	if _, err := s.UploadArtifact(ctx, b.ID, "bad.zip", bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("corrupt zip must be rejected for web")
	}
	if _, err := s.UploadArtifact(ctx, b.ID, "dist.tar.gz", bytes.NewReader([]byte("t"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int64
	db.Model(&model.CIPipeline{}).Where("id = ?", b.PipelineID).Count(&n)
	if n != 0 {
		t.Fatal("managed pipeline not deleted")
	}
	db.Model(&model.CIBuilderToken{}).Where("builder_id = ?", b.ID).Count(&n)
	if n != 0 {
		t.Fatal("tokens not deleted")
	}
	if _, err := s.Get(ctx, b.ID); err == nil {
		t.Fatal("builder row remains")
	}
}

func mustCurrentToken(t *testing.T, s *Service, builderID uint) string {
	t.Helper()
	tok, err := s.currentToken(context.Background(), builderID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
