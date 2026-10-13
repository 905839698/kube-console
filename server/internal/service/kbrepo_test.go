package service

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"kube-console/server/internal/kube"
)

// s3 provider 的参数清单照 1.0.2 实测对象裁剪：bucket/region 必填，
// accessKeyId/secretAccessKey 是凭证字段（进 Secret 不进 config），
// csiDriverName 指向 ru.yandex.s3.csi（没装则该 provider 的 Mount 方式不可用）。
func kbProviderS3(phase string, csiMsg string) *unstructured.Unstructured {
	conds := []any{}
	if csiMsg != "" {
		conds = append(conds, map[string]any{
			"type": "CSIDriverInstalled", "status": "Unknown",
			"reason": "CheckCSIDriverFailed", "message": csiMsg,
		})
	}
	return kbUD("s3", kbProviderGVR, "StorageProvider", "", map[string]any{
		"spec": map[string]any{
			"csiDriverName": "ru.yandex.s3.csi",
			"parametersSchema": map[string]any{
				"credentialFields": []any{"accessKeyId", "secretAccessKey"},
				"openAPIV3Schema": map[string]any{
					"type":     "object",
					"required": []any{"bucket", "region", "accessKeyId", "secretAccessKey"},
					"properties": map[string]any{
						"bucket":             map[string]any{"type": "string", "description": "S3 bucket"},
						"region":             map[string]any{"type": "string", "description": "AWS region, e.g. us-west-1"},
						"endpoint":           map[string]any{"type": "string", "description": "S3 endpoint (optional)"},
						"insecure":           map[string]any{"type": "boolean", "default": false},
						"geesefsMemoryLimit": map[string]any{"type": "integer", "default": int64(512)},
						"accessKeyId":        map[string]any{"type": "string", "description": "AWS access key"},
						"secretAccessKey":    map[string]any{"type": "string", "description": "AWS secret key"},
					},
				},
			},
		},
		"status": map[string]any{"phase": phase, "conditions": conds},
	})
}

func kbS3CredSecret(name, ns string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("x")
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       data,
	}
}

func kbRepoFixtureClient(c *fake.Clientset, objs ...*unstructured.Unstructured) *kube.Client {
	return &kube.Client{Dynamic: kbFakeDyn(objs...), Clientset: c}
}

func kbRepoToolInput(name string) KBRepoInput {
	return KBRepoInput{
		Name: name, Provider: "s3", AccessMethod: kbRepoTool, PVReclaimPolicy: "Delete",
		PathPrefix: "/" + name + "/",
		SecretName: "s3-cred", SecretNamespace: "kb-system",
		Config: map[string]string{"bucket": "kc-backup", "region": "cn-north-1"},
	}
}

// 表单数据源：参数清单与"哪些是凭证字段"必须从 StorageProvider 的 schema 里读出来，
// 控制台不写死 s3 的字段名
func TestKBStorageProvidersReadsSchema(t *testing.T) {
	c := kbRepoFixtureClient(fake.NewSimpleClientset(),
		kbProviderS3("NotReady", `CSIDriver.storage.k8s.io "ru.yandex.s3.csi" not found`))

	providers, err := KBStorageProviders(context.Background(), c)
	if err != nil {
		t.Fatalf("KBStorageProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("应有 1 个 provider，实际 %d", len(providers))
	}
	p := providers[0]
	if p.Phase != "NotReady" || !strings.Contains(p.Reason, "ru.yandex.s3.csi") {
		t.Errorf("phase/原因没带出来: %+v", p)
	}
	byName := map[string]KBProviderParam{}
	for _, q := range p.Params {
		byName[q.Name] = q
	}
	if !byName["bucket"].Required || byName["bucket"].Credential {
		t.Errorf("bucket 应是必填普通参数: %+v", byName["bucket"])
	}
	if !byName["accessKeyId"].Credential || !byName["accessKeyId"].Required {
		t.Errorf("accessKeyId 应是凭证字段（CRD 的 required 也包含它，但值落 Secret 不落 config）: %+v", byName["accessKeyId"])
	}
	if byName["endpoint"].Required {
		t.Errorf("endpoint 不在 required 列表里: %+v", byName["endpoint"])
	}
	if byName["insecure"].Default != "false" || byName["geesefsMemoryLimit"].Default != "512" {
		t.Errorf("默认值应带出来: insecure=%q mem=%q", byName["insecure"].Default, byName["geesefsMemoryLimit"].Default)
	}
	// 必填在前、凭证沉底，前端直接按这个顺序渲染
	if p.Params[0].Name != "bucket" && p.Params[0].Name != "region" {
		t.Errorf("必填参数应排在前面，实际第一个是 %s", p.Params[0].Name)
	}
	if !p.Params[len(p.Params)-1].Credential {
		t.Errorf("凭证字段应排在最后，实际最后一个是 %+v", p.Params[len(p.Params)-1])
	}
}

// provider 因缺 CSI 而 NotReady 时，Mount 方式必须在建仓库之前就被拒——
// 建成之后 operator 只会把仓库卡在 NotReady，用户看到的是一个"存在但不能用"的仓库
func TestKBCreateRepoRejectsMountWithoutCSI(t *testing.T) {
	c := kbRepoFixtureClient(fake.NewSimpleClientset(
		kbS3CredSecret("s3-cred", "kb-system", "accessKeyId", "secretAccessKey")),
		kbProviderS3("NotReady", `CSIDriver.storage.k8s.io "ru.yandex.s3.csi" not found`))

	in := kbRepoToolInput("kc-s3")
	in.AccessMethod = "Mount"
	in.VolumeCapacity = "100Gi"
	err := KBCreateBackupRepo(context.Background(), c, in)
	if err == nil {
		t.Fatal("CSI 驱动缺失时 Mount 应被拒绝")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Tool") || !strings.Contains(msg, "ru.yandex.s3.csi") {
		t.Errorf("错误应说清原因并指向 Tool 方式，实际: %s", msg)
	}
}

// Tool 方式（datasafed，不需要 CSI）+ 参数齐全 → 落库的 spec 形状
func TestKBCreateRepoWithToolMethod(t *testing.T) {
	c := kbRepoFixtureClient(fake.NewSimpleClientset(
		kbS3CredSecret("s3-cred", "kb-system", "accessKeyId", "secretAccessKey")),
		kbProviderS3("NotReady", "CSIDriver not found"))

	if err := KBCreateBackupRepo(context.Background(), c, kbRepoToolInput("kc-s3")); err != nil {
		t.Fatalf("KBCreateBackupRepo: %v", err)
	}
	obj, err := c.Dynamic.Resource(kbRepoGVR).Get(context.Background(), "kc-s3", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回仓库: %v", err)
	}
	if gvk := obj.GroupVersionKind(); gvk.Group != "dataprotection.kubeblocks.io" || gvk.Kind != "BackupRepo" {
		t.Errorf("apiVersion/kind 不对: %+v", gvk)
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec["storageProviderRef"] != "s3" || spec["accessMethod"] != kbRepoTool || spec["pvReclaimPolicy"] != "Delete" {
		t.Errorf("spec 基本字段不对: %+v", spec)
	}
	if spec["pathPrefix"] != "kc-s3" {
		t.Errorf("pathPrefix 应去掉首尾斜杠，实际 %v", spec["pathPrefix"])
	}
	cfg, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "config")
	if cfg["bucket"] != "kc-backup" || cfg["region"] != "cn-north-1" {
		t.Errorf("config 丢了参数: %+v", cfg)
	}
	cred, _, _ := unstructured.NestedMap(obj.Object, "spec", "credential")
	if cred["name"] != "s3-cred" || cred["namespace"] != "kb-system" {
		t.Errorf("credential 引用不对: %+v", cred)
	}
	if _, has := spec["volumeCapacity"]; has {
		t.Errorf("Tool 方式不该写 volumeCapacity: %+v", spec)
	}
}

// 参数写错（多一个字母）不会被 provider 拒绝，只会被静默忽略，备份时才发现连不上——
// 所以未知键、缺必填、凭证缺键都要在建之前明确报错
func TestKBCreateRepoValidatesParamsAndCredential(t *testing.T) {
	ok := kbS3CredSecret("s3-cred", "kb-system", "accessKeyId", "secretAccessKey")
	partial := kbS3CredSecret("s3-partial", "kb-system", "accessKeyId")
	t.Run("未知参数名", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(ok), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		in.Config["backet"] = "typo"
		err := KBCreateBackupRepo(context.Background(), c, in)
		if err == nil || !strings.Contains(err.Error(), "backet") || !strings.Contains(err.Error(), "bucket") {
			t.Fatalf("应指出未知键并列出可用参数，实际: %v", err)
		}
	})
	t.Run("缺必填参数", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(ok), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		delete(in.Config, "region")
		err := KBCreateBackupRepo(context.Background(), c, in)
		if err == nil || !strings.Contains(err.Error(), "region") {
			t.Fatalf("应提示 region 必填，实际: %v", err)
		}
	})
	t.Run("凭证 Secret 缺键", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(partial), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		in.SecretName = "s3-partial"
		err := KBCreateBackupRepo(context.Background(), c, in)
		if err == nil || !strings.Contains(err.Error(), "secretAccessKey") {
			t.Fatalf("应指出缺 secretAccessKey，实际: %v", err)
		}
	})
	t.Run("凭证 Secret 不存在", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(ok), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		in.SecretName = "nope"
		if err := KBCreateBackupRepo(context.Background(), c, in); err == nil {
			t.Fatal("应拒绝不存在的 Secret")
		}
	})
	t.Run("Mount 需要容量", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(ok), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		in.AccessMethod = "Mount"
		err := KBCreateBackupRepo(context.Background(), c, in)
		if err == nil || !strings.Contains(err.Error(), "volumeCapacity") {
			t.Fatalf("Mount 方式应要求 volumeCapacity，实际: %v", err)
		}
		in.VolumeCapacity = "abc"
		if err := KBCreateBackupRepo(context.Background(), c, in); err == nil {
			t.Fatal("非法容量值应被拒绝")
		}
	})
	t.Run("回收策略必填", func(t *testing.T) {
		c := kbRepoFixtureClient(fake.NewSimpleClientset(ok), kbProviderS3("Ready", ""))
		in := kbRepoToolInput("kc-s3")
		in.PVReclaimPolicy = ""
		if err := KBCreateBackupRepo(context.Background(), c, in); err == nil {
			t.Fatal("pvReclaimPolicy 缺失应被拒绝")
		}
	})
}

// provider 带 CEL 不可变校验，改 provider 只能删了重建；config 里 schema 认得的键
// 由表单权威覆盖（清空即取消设置），schema 外的手工键保留
func TestKBUpdateRepoKeepsProviderAndMergesConfig(t *testing.T) {
	existing := kbUD("kc-s3", kbRepoGVR, "BackupRepo", "", map[string]any{
		"spec": map[string]any{
			"storageProviderRef": "s3",
			"accessMethod":       kbRepoTool,
			"pvReclaimPolicy":    "Delete",
			"config": map[string]any{
				"bucket": "old-bucket", "region": "old-region",
				"vendorSpecific": "keep-me",
			},
		},
	})
	c := kbRepoFixtureClient(fake.NewSimpleClientset(
		kbS3CredSecret("s3-cred", "kb-system", "accessKeyId", "secretAccessKey")),
		kbProviderS3("NotReady", "CSIDriver not found"), existing)

	in := kbRepoToolInput("kc-s3")
	in.Config = map[string]string{"bucket": "new-bucket", "region": "cn-north-1"}
	if err := KBUpdateBackupRepo(context.Background(), c, in); err != nil {
		t.Fatalf("KBUpdateBackupRepo: %v", err)
	}
	cfg, _, _ := unstructured.NestedStringMap(mustRepo(t, c).Object, "spec", "config")
	if cfg["bucket"] != "new-bucket" || cfg["region"] != "cn-north-1" {
		t.Errorf("表单值应覆盖旧值: %+v", cfg)
	}
	if cfg["vendorSpecific"] != "keep-me" {
		t.Errorf("schema 外的手工键应保留: %+v", cfg)
	}

	in.Provider = "oss"
	err := KBUpdateBackupRepo(context.Background(), c, in)
	if err == nil || !strings.Contains(err.Error(), "不可修改") {
		t.Fatalf("换 provider 应被拒（CRD 的 CEL 校验），实际: %v", err)
	}
}

func mustRepo(t *testing.T, c *kube.Client) *unstructured.Unstructured {
	t.Helper()
	obj, err := c.Dynamic.Resource(kbRepoGVR).Get(context.Background(), "kc-s3", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("取回仓库: %v", err)
	}
	return obj
}

// 凭证 Secret 候选：只有键齐的才可选（不回内容），省得用户猜哪个 Secret 能用
func TestKBCredentialSecretsFiltersByFields(t *testing.T) {
	c := kbRepoFixtureClient(fake.NewSimpleClientset(
		kbS3CredSecret("b-full", "kb-system", "accessKeyId", "secretAccessKey"),
		kbS3CredSecret("a-partial", "kb-system", "accessKeyId"),
		kbS3CredSecret("c-full", "kb-system", "secretAccessKey", "accessKeyId", "extra"),
	), kbProviderS3("Ready", ""))

	got, err := KBCredentialSecrets(context.Background(), c, "kb-system", "s3")
	if err != nil {
		t.Fatalf("KBCredentialSecrets: %v", err)
	}
	names := []string{}
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "b-full,c-full" {
		t.Errorf("应只返回键齐的 Secret 并按名排序，实际 %v", names)
	}
}

func TestKBDeleteBackupRepo(t *testing.T) {
	existing := kbUD("kc-s3", kbRepoGVR, "BackupRepo", "", map[string]any{
		"spec": map[string]any{"storageProviderRef": "s3", "pvReclaimPolicy": "Delete"},
	})
	c := kbRepoFixtureClient(fake.NewSimpleClientset(), existing)
	if err := KBDeleteBackupRepo(context.Background(), c, "kc-s3"); err != nil {
		t.Fatalf("KBDeleteBackupRepo: %v", err)
	}
	if err := KBDeleteBackupRepo(context.Background(), c, "kc-s3"); err == nil {
		t.Fatal("删不存在的仓库应报错而不是静默成功")
	}
	if err := KBDeleteBackupRepo(context.Background(), c, "Bad Name"); err == nil {
		t.Fatal("名字不合法应先拦住")
	}
}

// 真实集群核对（只读）：KC_TEST_KUBECONFIG=~/.kube/config go test ./internal/service -run LiveRepo
// 断的是 schema 解析口径：s3 provider 的必填参数、凭证字段与 CSI 未就绪原因必须从
// StorageProvider 对象里读出来。addon 改写法（或换成 camelCase 字段）时这里会先响。
func TestKBStorageProvidersLive(t *testing.T) {
	path := os.Getenv("KC_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("未设 KC_TEST_KUBECONFIG，跳过真实集群核对")
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 kubeconfig: %v", err)
	}
	cl, err := kube.NewClient(string(bs))
	if err != nil {
		t.Skipf("连不上集群: %v", err)
	}
	providers, err := KBStorageProviders(context.Background(), cl)
	if err != nil {
		t.Fatalf("KBStorageProviders: %v", err)
	}
	if len(providers) == 0 {
		t.Skip("该集群未装 KubeBlocks，没有 StorageProvider")
	}
	var s3 *KBProviderView
	for i := range providers {
		t.Logf("%s: phase=%s csi=%s params=%d reason=%q", providers[i].Name, providers[i].Phase,
			providers[i].CSIDriver, len(providers[i].Params), providers[i].Reason)
		if providers[i].Name == "s3" {
			s3 = &providers[i]
		}
	}
	if s3 == nil {
		t.Fatal("没找到 s3 provider，仓库表单没法默认选它")
	}
	if len(s3.Params) == 0 {
		t.Fatal("s3 的 parametersSchema 没解析出任何字段（表单会是空的）")
	}
	need := map[string]KBProviderParam{}
	for _, p := range s3.Params {
		need[p.Name] = p
	}
	for _, k := range []string{"bucket", "region", "endpoint"} {
		if _, ok := need[k]; !ok {
			t.Errorf("s3 参数里应有 %s", k)
		}
	}
	for _, k := range []string{"accessKeyId", "secretAccessKey"} {
		p, ok := need[k]
		if !ok || !p.Credential {
			t.Errorf("%s 应被识别为凭证字段（值进 Secret）: %+v", k, p)
		}
	}
	if s3.Phase != "Ready" && !strings.Contains(s3.Reason, "csi") {
		t.Errorf("未就绪的 provider 应带出原因，实际 %q", s3.Reason)
	}
	// 凭证候选：不关心有没有，只要求不报错（多半是空列表，因为还没人建过 S3 Secret）
	if _, err := KBCredentialSecrets(context.Background(), cl, "kb-system", "s3"); err != nil {
		t.Errorf("KBCredentialSecrets: %v", err)
	}
}
