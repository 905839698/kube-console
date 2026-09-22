package handler

// ArgoCD 仓库管理（v3.x：Repository CRD 已移除，仓库+凭据以 Secret 形式存储）
//
// 经现网 ArgoCD v3.5.2 实测确认的格式：
//   - Secret 需带标签 argocd.argoproj.io/secret-type，无标签不识别；两种取值（均经 v3.5.2 实测）：
//     * repository  —— 完整仓库（URL 含路径），ArgoCD 对其做连通性测试
//     * repo-creds  —— 凭据模板（URL 仅主机，如 http://gitlab.xxx.com），
//                      该域下所有仓库的 Application 自动继承其账号密码（继承已实测生效）
//   - 数据键：url / type(git|helm，仅 repository 需要) / username / password /
//     ssh-private-key / insecure / enableLfs
//   - 命名建议 repo-<name>（ArgoCD 官方约定）
//
// 读接口登录可见（不回显密码/私钥明文），写接口 admin（与 ArgoCD 应用策略一致）。

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kube-console/server/internal/kube"
	"kube-console/server/pkg/response"
)

const argoRepoSecretTypeLabel = "argocd.argoproj.io/secret-type"

// 主机级 URL（无路径）存为凭据模板；带路径的完整仓库 URL 存为 repository
func repoURLIsHostOnly(u string) bool {
	p, err := url.Parse(u)
	return err != nil || p.Path == "" || p.Path == "/"
}

var repoNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,251}[a-z0-9])?$`)

// argoRepo 列表项。凭据只返回有无标志，不返回密码/私钥明文。
type argoRepo struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	RepoType       string `json:"type"` // git / helm
	Username       string `json:"username,omitempty"`
	HasPassword    bool   `json:"hasPassword"`
	HasSSHKey      bool   `json:"hasSSHKey"`
	Insecure       bool   `json:"insecure"`
	EnableLfs      bool   `json:"enableLfs"`
	CredentialOnly bool   `json:"credentialOnly"` // 主机级 URL（凭据模板，作用于该域下所有仓库）
}

// argocdNamespace 定位 ArgoCD 部署所在命名空间（按 argocd-server Deployment 探测）。
// 探测不到 / 列表失败返回明确错误——静默回退 "argocd" 会让仓库列表在错误 ns 上
// 返回空（界面显示 0 个仓库）、创建/更新落到不存在的 ns 报莫名 404。
func argocdNamespace(ctx context.Context, client *kube.Client) (string, error) {
	var continueToken string
	for {
		dls, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{Limit: 500, Continue: continueToken})
		if err != nil {
			return "", err
		}
		for i := range dls.Items {
			n := dls.Items[i].Name
			if n == "argocd-server" || n == "argocd-application-controller" || strings.HasPrefix(n, "argocd-server-") {
				return dls.Items[i].Namespace, nil
			}
		}
		if dls.Continue == "" {
			break
		}
		continueToken = dls.Continue
	}
	return "", errors.New("未探测到 ArgoCD 部署（找不到 argocd-server Deployment）：请确认 ArgoCD 已部署，或在请求中显式指定 namespace 参数")
}

// ArgoRepos GET /argocd/repos —— 列出所有仓库 Secret
func (h *CIHandler) ArgoRepos(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	ns, err := argocdNamespace(ctx, client)
	if err != nil {
		response.Fail(c, 500, 500, "定位 ArgoCD 命名空间失败: "+err.Error())
		return
	}
	// 诚实的 installed 判定：带标签的 Secret 列表在 CRD 不存在时不会 NoMatch，
	// 不能用来判断 ArgoCD 是否部署——按 applications CRD 判定（与 /argocd/apps 一致）
	installed := true
	if _, merr := client.Dynamic.Resource(argocdGVR).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); merr != nil && meta.IsNoMatchError(merr) {
		installed = false
	}
	secs, err := client.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: argoRepoSecretTypeLabel + " in (repository,repo-creds)",
	})
	if err != nil {
		response.K8sError(c, err)
		return
	}
	items := []argoRepo{}
	for i := range secs.Items {
		s := &secs.Items[i]
		isCreds := s.Labels[argoRepoSecretTypeLabel] == "repo-creds"
		items = append(items, argoRepoFromSecret(s.Namespace, s.Name, s.Data, isCreds))
	}
	response.OK(c, gin.H{"installed": installed, "namespace": ns, "items": items})
}

func argoRepoFromSecret(ns, name string, data map[string][]byte, credentialOnly bool) argoRepo {
	r := argoRepo{
		Namespace:      ns,
		Name:           strings.TrimPrefix(name, "repo-"),
		RepoType:       "git",
		CredentialOnly: credentialOnly,
	}
	r.URL = string(data["url"])
	if t := string(data["type"]); t != "" {
		r.RepoType = t
	}
	r.Username = string(data["username"])
	r.HasPassword = len(data["password"]) > 0
	r.HasSSHKey = len(data["ssh-private-key"]) > 0
	r.Insecure = string(data["insecure"]) == "true"
	r.EnableLfs = string(data["enableLfs"]) == "true"
	return r
}

// errRepoSecretNotFound 按展示名解析不到对应 Secret（不是 K8s 通用 NotFound，
// 调用方据此区分「确实没有」与「查询失败」）
var errRepoSecretNotFound = errors.New("repo secret not found")

// resolveArgoRepoSecret 按列表展示名解析真实 Secret：先按本平台约定 repo-<name> 取，
// 再回退到「去前缀名 == name」的带标签 Secret（覆盖 ArgoCD 原生注册的仓库——其
// Secret 名是净化后的 URL、不带 repo- 前缀，但同样出现在列表里）。
// 只按 repo-<name> 回查会让编辑这类仓库时新建一个平行的重复 Secret（原凭据不动，
// 列表出现两条同 URL）、删除时静默成功（真正的 Secret 没被删）。
func resolveArgoRepoSecret(ctx context.Context, client *kube.Client, ns, name string) (*corev1.Secret, error) {
	s, err := client.Clientset.CoreV1().Secrets(ns).Get(ctx, "repo-"+name, metav1.GetOptions{})
	if err == nil {
		return s, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	secs, lerr := client.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: argoRepoSecretTypeLabel + " in (repository,repo-creds)",
	})
	if lerr != nil {
		return nil, lerr
	}
	for i := range secs.Items {
		if strings.TrimPrefix(secs.Items[i].Name, "repo-") == name {
			return &secs.Items[i], nil
		}
	}
	return nil, errRepoSecretNotFound
}

// ArgoRepoSaveReq 新建/更新仓库。
// AuthType：http / ssh / none 显式声明认证形态——更新时按它重置凭据（切到 http 会清掉
// 残留的 ssh-private-key，切到 none 清掉全部），避免「留空=保留」把上一形态的凭据
// 永远留在 Secret 里；留空（旧客户端）保持历史合并语义。
type ArgoRepoSaveReq struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"` // 新建时必填（正则校验）；更新时取路径参数
	URL           string `json:"url" binding:"required"`
	Type          string `json:"type"`
	AuthType      string `json:"authType"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	SshPrivateKey string `json:"sshPrivateKey"`
	Insecure      bool   `json:"insecure"`
	EnableLfs     bool   `json:"enableLfs"`
}

func (h *CIHandler) validateArgoRepo(c *gin.Context, r *ArgoRepoSaveReq) bool {
	if !repoNameRe.MatchString(r.Name) {
		response.Fail(c, 400, 400, "仓库名须为 RFC 1123 小写字母/数字/中划线")
		return false
	}
	if r.Type == "" {
		r.Type = "git"
	}
	if r.Type != "git" && r.Type != "helm" {
		response.Fail(c, 400, 400, "仓库类型只能是 git 或 helm")
		return false
	}
	if _, err := url.ParseRequestURI(r.URL); err != nil {
		response.Fail(c, 400, 400, "仓库 URL 非法: "+err.Error())
		return false
	}
	return true
}

func repoSecretData(r *ArgoRepoSaveReq, username, password, sshKey string) map[string][]byte {
	data := map[string][]byte{
		"url": []byte(r.URL),
	}
	if !repoURLIsHostOnly(r.URL) {
		data["type"] = []byte(r.Type)
	}
	if username != "" {
		data["username"] = []byte(username)
	}
	if password != "" {
		data["password"] = []byte(password)
	}
	if sshKey != "" {
		data["ssh-private-key"] = []byte(sshKey)
	}
	if r.Insecure {
		data["insecure"] = []byte("true")
	}
	if r.EnableLfs {
		data["enableLfs"] = []byte("true")
	}
	return data
}

// ArgoRepoCreate POST /argocd/repos
func (h *CIHandler) ArgoRepoCreate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var r ArgoRepoSaveReq
	if err := c.ShouldBindJSON(&r); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	if r.Namespace == "" {
		var err error
		r.Namespace, err = argocdNamespace(c.Request.Context(), client)
		if err != nil {
			response.Fail(c, 500, 500, "定位 ArgoCD 命名空间失败: "+err.Error())
			return
		}
	}
	if !h.validateArgoRepo(c, &r) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	// 名称校验正则允许到 253，但 Secret 名是 repo-<name>（会到 258）——超 K8s
	// 对象名 253 上限时 API server 只给一句无名错误，这里提前报清楚
	if len("repo-"+r.Name) > 253 {
		response.Fail(c, 400, 400, "仓库名过长：加 repo- 前缀后超过 K8s 对象名 253 字符上限")
		return
	}
	secretName := "repo-" + r.Name
	if _, err := client.Clientset.CoreV1().Secrets(r.Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: r.Namespace,
			Labels:    map[string]string{argoRepoSecretTypeLabel: repoSecretLabel(r.URL)},
		},
		Data: repoSecretData(&r, r.Username, r.Password, r.SshPrivateKey),
	}, metav1.CreateOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"name": r.Name})
}

// ArgoRepoUpdate PUT /argocd/repos/:name —— upsert：不存在则创建，存在则更新
// （前端新建/编辑统一走此接口，避免客户端状态导致建/改错配）。
// 按展示名解析真实 Secret（repo-<name> 或 ArgoCD 原生命名），更新落回同一个
// Secret，不产生平行副本。
func (h *CIHandler) ArgoRepoUpdate(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	var r ArgoRepoSaveReq
	if err := c.ShouldBindJSON(&r); err != nil {
		response.Fail(c, 400, 400, "参数错误: "+err.Error())
		return
	}
	if r.Namespace == "" {
		var err error
		r.Namespace, err = argocdNamespace(c.Request.Context(), client)
		if err != nil {
			response.Fail(c, 500, 500, "定位 ArgoCD 命名空间失败: "+err.Error())
			return
		}
	}
	r.Name = c.Param("name")
	if !h.validateArgoRepo(c, &r) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	existing, err := resolveArgoRepoSecret(ctx, client, r.Namespace, r.Name)
	if errors.Is(err, errRepoSecretNotFound) {
		// upsert：不存在 → 直接创建（凭据按表单原值）
		secretName := "repo-" + r.Name
		if len(secretName) > 253 {
			response.Fail(c, 400, 400, "仓库名过长：加 repo- 前缀后超过 K8s 对象名 253 字符上限")
			return
		}
		if _, cerr := client.Clientset.CoreV1().Secrets(r.Namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: r.Namespace,
				Labels:    map[string]string{argoRepoSecretTypeLabel: repoSecretLabel(r.URL)},
			},
			Data: repoSecretData(&r, r.Username, r.Password, r.SshPrivateKey),
		}, metav1.CreateOptions{}); cerr != nil {
			response.K8sError(c, cerr)
			return
		}
		response.OK(c, gin.H{"name": r.Name})
		return
	}
	if err != nil {
		response.K8sError(c, err)
		return
	}
	// 凭据语义：AuthType 显式声明（http/ssh/none）→ 按新形态重置，清掉旧形态残留
	// （切 http 清 ssh 密钥、切 none 清全部；密码/私钥留空 = 保持原值）；
	// 留空 = 旧客户端的合并语义（全部留空 = 全保留）
	username, password, sshKey := "", "", ""
	switch r.AuthType {
	case "http":
		username, password = r.Username, r.Password
		if password == "" {
			if v, ok := existing.Data["password"]; ok {
				password = string(v)
			}
		}
		if username == "" {
			if v, ok := existing.Data["username"]; ok {
				username = string(v)
			}
		}
	case "ssh":
		sshKey = r.SshPrivateKey
		if sshKey == "" {
			if v, ok := existing.Data["ssh-private-key"]; ok {
				sshKey = string(v)
			}
		}
	case "none":
		// 匿名：不保留任何凭据
	default:
		if v, ok := existing.Data["username"]; ok {
			username = string(v)
		}
		if r.Username != "" {
			username = r.Username
		}
		if v, ok := existing.Data["password"]; ok {
			password = string(v)
		}
		if r.Password != "" {
			password = r.Password
		}
		if v, ok := existing.Data["ssh-private-key"]; ok {
			sshKey = string(v)
		}
		if r.SshPrivateKey != "" {
			sshKey = r.SshPrivateKey
		}
	}
	// 只覆盖表单建模的键，保留其余键——ArgoCD 原生注册/扩展认证的仓库带
	// github-app-*、TLS client-config、http-proxy 等键，整块替换会静默擦除它们
	// 使仓库认证立刻失效
	managed := []string{"url", "type", "username", "password", "ssh-private-key", "insecure", "enableLfs"}
	data := make(map[string][]byte, len(existing.Data)+len(managed))
	for k, v := range existing.Data {
		data[k] = v
	}
	for _, k := range managed {
		delete(data, k)
	}
	for k, v := range repoSecretData(&r, username, password, sshKey) {
		data[k] = v
	}
	existing.Data = data
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[argoRepoSecretTypeLabel] = repoSecretLabel(r.URL)
	if _, err := client.Clientset.CoreV1().Secrets(existing.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"name": r.Name})
}

func repoSecretLabel(repoURL string) string {
	if repoURLIsHostOnly(repoURL) {
		return "repo-creds"
	}
	return "repository"
}

// ArgoRepoDelete DELETE /argocd/repos/:name?namespace= —— 按展示名解析真实 Secret
// 再删（ArgoCD 原生注册的不带 repo- 前缀）；解析不到报 404 而不是静默成功
// （此前直接删 repo-<name> 并吞 NotFound，对这类仓库会提示「已删除」但实际没删）
func (h *CIHandler) ArgoRepoDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns := c.Query("namespace")
	if ns == "" {
		var err error
		ns, err = argocdNamespace(c.Request.Context(), client)
		if err != nil {
			response.Fail(c, 500, 500, "定位 ArgoCD 命名空间失败: "+err.Error())
			return
		}
	}
	name := c.Param("name")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	s, err := resolveArgoRepoSecret(ctx, client, ns, name)
	if errors.Is(err, errRepoSecretNotFound) {
		response.Fail(c, 404, 404, "仓库不存在（未找到对应 Secret，可能已被删除）")
		return
	}
	if err != nil {
		response.K8sError(c, err)
		return
	}
	if err := client.Clientset.CoreV1().Secrets(s.Namespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"name": name})
}
