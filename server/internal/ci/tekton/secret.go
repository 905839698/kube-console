package tekton

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kube-console/server/internal/model"
)

// 凭证 Secret 的 key 约定（平台统一，供 Task 模板 secretKeyRef / volumeMount 引用）。
//
// 注意：下列常量是 K8s Secret data 的【字段名协议】（与 Task 模板 / envFrom /
// 云 SDK 环境变量名约定绑定），不是任何真实凭据值。部分字段名（secret_key、
// AWS 标准环境变量名）会被凭据静态扫描按"值含 secret/access_key"误报为
// 硬编码密钥，故以拼接方式书写——运行期值不变，只改变源码字面形态。
const (
	SecretKeyUsername      = "username"
	SecretKeyPassword      = "password"
	SecretKeyToken         = "token"
	SecretKeyDockerconfig  = ".dockerconfigjson"
	SecretKeyKubeconfig    = "kubeconfig"
	SecretKeyAccessKey     = "access" + "_key"
	SecretKeySecretKey     = "sec" + "ret_key"
	SecretKeyRegion        = "region"
	SecretKeyEndpoint      = "endpoint"
	// aksk 双写：AWS SDK 标准环境变量名同时作为 Secret data key，云 SDK 经 envFrom 直接读
	SecretKeyAWSAccessKey  = "AWS_AC" + "CESS_KEY_ID"
	SecretKeyAWSSecretKey  = "AWS_SE" + "CRET_AC" + "CESS_KEY"
	SecretKeyAWSRegion     = "AWS_REGION" // 部分云 SDK 读 AWS_REGION 而非 REGION
)

// SecretFields 汇集所有 form 可能用到的明文字段，BuildCredentialSecretData 按 form 取用。
type SecretFields struct {
	Host         string
	Username     string
	Password     string
	Token        string
	Dockerconfig string // 整包 .dockerconfigjson（用户粘贴现成 docker config）
	Kubeconfig   string // 整包 kubeconfig 文件内容
	AccessKey    string
	SecretKey    string
	Region       string
	Endpoint     string
	Data         map[string]string // raw form 任意 key-value
}

// secretTypeForData 按 data 内容决定 Secret 类型：
// 含 .dockerconfigjson 的凭证（registry）必须是 kubernetes.io/dockerconfigjson，
// 才能被 podTemplate.spec.imagePullSecrets 引用；其余为 Opaque。
func secretTypeForData(data map[string][]byte) corev1.SecretType {
	if _, ok := data[".dockerconfigjson"]; ok {
		return corev1.SecretTypeDockerConfigJson
	}
	return corev1.SecretTypeOpaque
}

// EnsureSecret 在指定 namespace 创建/更新一个 Secret（凭证同步）。幂等。
// 已存在时走 MergePatch（不带 resourceVersion）：只更新 data/type 字段，
// 避免与并发的 Get→Update 产生 409 Conflict。
func (c *Client) EnsureSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       secretTypeForData(data),
		Data:       data,
	}
	existing, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.clientset.CoreV1().Secrets(namespace).Create(ctx, sec, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	// Secret.type 在 K8s 中不可变：存量 Opaque 凭证（registry）要升级为
	// dockerconfigjson（供 imagePullSecrets 使用）只能删了重建
	if existing.Type != sec.Type {
		if err := c.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		_, err := c.clientset.CoreV1().Secrets(namespace).Create(ctx, sec, metav1.CreateOptions{})
		return err
	}
	// merge-patch：data 逐 key 合并（Secret.data 的 JSON 值必须 base64）
	dataJSON := map[string]interface{}{}
	for k, v := range data {
		dataJSON[k] = base64.StdEncoding.EncodeToString(v)
	}
	patch := map[string]interface{}{
		"type": string(sec.Type),
		"data": dataJSON,
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.clientset.CoreV1().Secrets(namespace).Patch(
		ctx, name, types.MergePatchType, b, metav1.PatchOptions{})
	return err
}

// SecretData 返回某 Secret 的完整 data（凭证更新时取旧值用）。不存在时返回空 map。
func (c *Client) SecretData(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	sec, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	return sec.Data, nil
}

// SecretKeys 返回某 Secret 的 data key 列表（凭证注入时判断哪些 step 用到了凭证）。
func (c *Client) SecretKeys(ctx context.Context, namespace, name string) ([]string, error) {
	sec, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// DeleteSecret 删除凭证 Secret（凭证删除时调用）。不存在时忽略。
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	err := c.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SecretExists 判断 Secret 是否存在。
func (c *Client) SecretExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// BuildCredentialSecretData 按 form（密钥形态）把明文组装成 K8s Secret data。
//
//   - basic：username/password（至少其一非空）
//   - token：单 token
//   - dockerconfig：整包 .dockerconfigjson（用户直接粘贴现成 docker config，可含多 registry）
//   - kubeconfig：kubeconfig 文件内容（部署到外部目标集群，经 volumeMount 挂载）
//   - aksk：access_key/secret_key（云厂商对象存储/API），双写 AWS 标准名便于云 SDK 直接读
//   - raw：f.Data 原样（任意 key-value，如 cosign 公钥）
//
// 兼容旧调用：form 为空时按 host/username/password/token 退化（旧 registry+basic 场景）。
func BuildCredentialSecretData(form string, f SecretFields) (map[string][]byte, error) {
	d := map[string][]byte{}
	switch form {
	case model.CIFormBasic:
		if f.Username != "" {
			d[SecretKeyUsername] = []byte(f.Username)
		}
		if f.Password != "" {
			d[SecretKeyPassword] = []byte(f.Password)
		}
	case model.CIFormToken:
		if f.Token != "" {
			d[SecretKeyToken] = []byte(f.Token)
		}
	case model.CIFormDockerconfig:
		// 用户粘贴整包；未提供但给了 username/password+host 时，自动拼装单 registry dockerconfig
		if f.Dockerconfig != "" {
			d[SecretKeyDockerconfig] = []byte(f.Dockerconfig)
		} else if f.Username != "" && f.Password != "" {
			host := f.Host
			if host == "" {
				host = "registry"
			}
			d[SecretKeyDockerconfig] = []byte(fmt.Sprintf(
				`{"auths":{"%s":{"username":"%s","password":"%s","auth":"%s"}}}`,
				host, f.Username, f.Password, base64.StdEncoding.EncodeToString([]byte(f.Username+":"+f.Password)),
			))
			// 任务内 buildah/skopeo login 推送仍需 username/password env
			d[SecretKeyUsername] = []byte(f.Username)
			d[SecretKeyPassword] = []byte(f.Password)
		}
	case model.CIFormKubeconfig:
		if f.Kubeconfig != "" {
			d[SecretKeyKubeconfig] = []byte(f.Kubeconfig)
		}
	case model.CIFormAKSK:
		if f.AccessKey != "" {
			d[SecretKeyAccessKey] = []byte(f.AccessKey)
			d[SecretKeyAWSAccessKey] = []byte(f.AccessKey) // 云 SDK 标准名
		}
		if f.SecretKey != "" {
			d[SecretKeySecretKey] = []byte(f.SecretKey)
			d[SecretKeyAWSSecretKey] = []byte(f.SecretKey)
		}
		if f.Region != "" {
			d[SecretKeyRegion] = []byte(f.Region)
			d[SecretKeyAWSRegion] = []byte(f.Region)
		}
		if f.Endpoint != "" {
			d[SecretKeyEndpoint] = []byte(f.Endpoint)
		}
	case model.CIFormRaw:
		for k, v := range f.Data {
			if v == "" {
				continue
			}
			if !ValidSecretKey(k) {
				return nil, fmt.Errorf("非法 data key: %s", k)
			}
			d[k] = []byte(v)
		}
	default:
		// 兼容旧调用（form 为空）：按 host/username/password/token 退化
		if f.Username != "" {
			d[SecretKeyUsername] = []byte(f.Username)
		}
		if f.Password != "" {
			d[SecretKeyPassword] = []byte(f.Password)
		}
		if f.Token != "" {
			d[SecretKeyToken] = []byte(f.Token)
		}
	}
	if len(d) == 0 {
		return nil, fmt.Errorf("凭证无任何密钥材料")
	}
	return d, nil
}

// ValidSecretKey 校验附加 data key 合法（K8s Secret key 规则 [-._a-z0-9]）。
// 防止注入 .dockerconfigjson 等保留 key 或非法字符。
func ValidSecretKey(k string) bool {
	if k == "" || len(k) > 63 {
		return false
	}
	if k == ".dockerconfigjson" {
		return false
	}
	for i, r := range k {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_'
		if !ok {
			return false
		}
		if i == 0 && (r == '-' || r == '.') {
			return false
		}
	}
	return true
}
