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
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// argocdNamespace 定位 ArgoCD 部署所在命名空间（按 argocd-server Deployment 探测，默认 argocd）
func argocdNamespace(ctx context.Context, client *kube.Client) string {
	dls, err := client.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{Limit: 500})
	if err == nil {
		for i := range dls.Items {
			n := dls.Items[i].Name
			if n == "argocd-server" || n == "argocd-application-controller" || strings.HasPrefix(n, "argocd-server-") {
				return dls.Items[i].Namespace
			}
		}
	}
	return "argocd"
}

// ArgoRepos GET /argocd/repos —— 列出所有仓库 Secret
func (h *CIHandler) ArgoRepos(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	ns := argocdNamespace(ctx, client)
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
	response.OK(c, gin.H{"installed": true, "namespace": ns, "items": items})
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

// ArgoRepoSaveReq 新建/更新仓库。更新时 Password/SshPrivateKey 留空 = 保留原凭据。
type ArgoRepoSaveReq struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"` // 新建时必填（正则校验）；更新时取路径参数
	URL           string `json:"url" binding:"required"`
	Type          string `json:"type"`
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
		r.Namespace = argocdNamespace(c.Request.Context(), client)
	}
	if !h.validateArgoRepo(c, &r) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
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

// ArgoRepoUpdate PUT /argocd/repos/:name —— upsert：不存在则创建，存在则合并更新
// （前端新建/编辑统一走此接口，避免客户端状态导致建/改错配）
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
		r.Namespace = argocdNamespace(c.Request.Context(), client)
	}
	r.Name = c.Param("name")
	if !h.validateArgoRepo(c, &r) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	secretName := "repo-" + r.Name
	existing, err := client.Clientset.CoreV1().Secrets(r.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// upsert：不存在 → 直接创建（凭据按表单原值）
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
	// 凭据合并：表单留空 = 保留原账号密码/私钥
	username, password, sshKey := "", "", ""
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
	existing.Data = repoSecretData(&r, username, password, sshKey)
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[argoRepoSecretTypeLabel] = repoSecretLabel(r.URL)
	if _, err := client.Clientset.CoreV1().Secrets(r.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
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

// ArgoRepoDelete DELETE /argocd/repos/:name?namespace=
func (h *CIHandler) ArgoRepoDelete(c *gin.Context) {
	client := h.kubeClient(c)
	if client == nil {
		return
	}
	ns := c.Query("namespace")
	if ns == "" {
		ns = argocdNamespace(c.Request.Context(), client)
	}
	name := c.Param("name")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := client.Clientset.CoreV1().Secrets(ns).Delete(ctx, "repo-"+name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		response.K8sError(c, err)
		return
	}
	response.OK(c, gin.H{"name": name})
}
