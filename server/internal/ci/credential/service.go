package credential

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gorm.io/gorm"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"kube-console/server/internal/config"
	"kube-console/server/internal/model"
	"kube-console/server/internal/ci/nodetype"
	pipelinesvc "kube-console/server/internal/ci/pipeline"
	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/tekton"
	"kube-console/server/internal/ci/errcode"
)

// Service 凭证管理。核心原则：平台只存引用+元数据，明文同步写入 K8s Secret。
type Service struct {
	db  *gorm.DB
	cfg *config.CIConfig
	// k8sFor 按集群取 Tekton 客户端（Secret 同步用）
	k8sFor func(cluster string) (*tekton.Client, error)
	reg nodetype.Registry // 节点注册表：按 schema 的 credential 属性判断引用
}

func NewService(db *gorm.DB, cfg *config.CIConfig, k8sFor func(cluster string) (*tekton.Client, error), reg nodetype.Registry) *Service {
	return &Service{db: db, cfg: cfg, k8sFor: k8sFor, reg: reg}
}

// secretNamespaces 返回该集群下凭证 Secret 需要同步的全部命名空间：
// 平台 ns + 该集群全部项目 ns。secretRef 不支持跨 ns，而 run 落在项目 ns，
// 因此 Secret 必须与 run 同 ns——创建/更新/删除时全量扇出。
func (s *Service) secretNamespaces(ctx context.Context, cluster string) []string {
	out := []string{s.cfg.PlatformNS}
	var nsList []string
	_ = s.db.WithContext(ctx).Model(&model.CIProject{}).
		Where("cluster_name = ?", cluster).Distinct().Pluck("namespace", &nsList).Error
	seen := map[string]bool{s.cfg.PlatformNS: true}
	for _, ns := range nsList {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	return out
}

// syncSecret 把 Secret 同步到集群内全部目标 ns。
func (s *Service) syncSecret(ctx context.Context, k8s *tekton.Client, cluster, name string, data map[string][]byte) error {
	var lastErr error
	for _, ns := range s.secretNamespaces(ctx, cluster) {
		if err := k8s.EnsureSecret(ctx, ns, name, data); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// deleteSecretAll 从集群内全部目标 ns 删除 Secret（best effort）。
func (s *Service) deleteSecretAll(ctx context.Context, cluster, name string) {
	k8s, err := s.k8sFor(cluster)
	if err != nil {
		return
	}
	for _, ns := range s.secretNamespaces(ctx, cluster) {
		_ = k8s.DeleteSecret(ctx, ns, name)
	}
}

// EnsureInNamespace 把凭证 Secret 对齐到 run 所在命名空间。幂等。
// 仅靠创建/更新时的扇出同步不可靠：项目 ns 的副本可能缺失（新建项目），
// 也可能存在但过期（扇出某次失败/漏发后一直留着旧版本——旧副本 envFrom
// 取不到新加的 username/password key，Pod 起得来但任务里没有凭证，表现为
// "未注入 registry 凭证" WARN + 401）。这里每次 run 都从平台 ns 的权威副本
// （凭证保存时的固定落点）强制对齐（EnsureSecret 合并 patch，类型变更时删建）；
// 平台 ns 副本也缺失（Secret 被外部删除）时返回明确错误，引导重新保存凭证。
func (s *Service) EnsureInNamespace(ctx context.Context, cluster, namespace string, c *model.CICredential) error {
	k8s, err := s.k8sFor(cluster)
	if err != nil {
		return errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	data, err := k8s.SecretData(ctx, c.SecretNS, c.SecretName)
	if err != nil {
		return errcode.Newf(errcode.DepUnavailable, "读取凭证 Secret 失败: %v", err)
	}
	if len(data) == 0 {
		return errcode.Newf(errcode.NotFound,
			"凭证「%s」的密钥材料缺失（Secret %s 在平台命名空间 %s 不存在，可能被外部删除），请到 CI 凭证管理重新保存该凭证",
			c.Name, c.SecretName, c.SecretNS)
	}
	if err := k8s.EnsureSecret(ctx, namespace, c.SecretName, data); err != nil {
		return errcode.Newf(errcode.DepUnavailable, "同步凭证 Secret 到命名空间 %s 失败: %v", namespace, err)
	}
	return nil
}

// credParamValues 返回节点实例中所有 type=credential 属性对应的凭证引用值。
// 只有这些属性名承载凭证引用，扫全部 string 参数值会把任意字符串误判成凭证名。
// 值形态：string（凭证名，标准）或 float64（前端早期提交的凭证 id，JSON 数字）——
// 两种都要收集，否则 id 形态的引用完全绕过删除守卫。
func credParamValues(reg nodetype.Registry, n dsl.Node) []string {
	if reg == nil || n.Params == nil {
		return nil
	}
	plugin, ok := reg.Get(n.Type)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range plugin.Meta().Properties {
		if p.Type != "credential" {
			continue
		}
		switch v := n.Params[p.Name].(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case float64:
			out = append(out, strconv.FormatInt(int64(v), 10))
		}
	}
	return out
}

// CreateReq 创建凭证请求。明文只在此处出现，立即写入 K8s Secret 后丢弃。
// Form 是密钥形态（basic/token/dockerconfig/kubeconfig/aksk/raw），决定 Secret 数据结构
// 与运行时注入策略；Type 是业务用途标签（可选，兼容旧数据）。
type CreateReq struct {
	Cluster      string            `json:"-"`                          // 所属集群：handler 从 X-Cluster 注入，不接受客户端传入
	Name         string            `json:"name" binding:"required"`
	Form         string            `json:"form" binding:"required"` // basic/token/dockerconfig/kubeconfig/aksk/raw
	Type         string            `json:"type"`                    // 业务用途标签（git/registry/k8s/cloud/generic，可选）
	Host         string            `json:"host"`                    // registry/git host（非敏感，进 Extra）
	Username     string            `json:"username"`                // basic / dockerconfig(自动拼装时)
	Password     string            `json:"password"`                // basic / dockerconfig(自动拼装时)
	Token        string            `json:"token"`                   // token
	Dockerconfig string            `json:"dockerconfig"`            // 整包 .dockerconfigjson
	Kubeconfig   string            `json:"kubeconfig"`             // 整包 kubeconfig
	AccessKey    string            `json:"accessKey"`               // aksk
	SecretKey    string            `json:"secretKey"`              // aksk
	Region       string            `json:"region"`                  // aksk 可选
	Endpoint     string            `json:"endpoint"`               // aksk 可选
	Data         map[string]string `json:"data"`                    // raw 任意 key-value
	ProjectID    *uint             `json:"projectId"`              // nil=平台级
}

// 校验凭证 form 合法。
var validForms = map[string]bool{
	model.CIFormBasic: true, model.CIFormToken: true, model.CIFormDockerconfig: true,
	model.CIFormKubeconfig: true, model.CIFormAKSK: true, model.CIFormRaw: true,
}

// credNameRe 凭证名会拼进 K8s Secret 名（cred-<name>），必须是 DNS-1123 子域，
// 否则 EnsureSecret 被 API server 拒绝，用户只能看到误导性的「写入 K8s Secret 失败」。
var credNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// formFromType 旧数据/旧请求无 form 时按 type 推断（registry→basic，cosign→raw，minio→aksk）。
func formFromType(t string) string {
	switch t {
	case "cosign":
		return model.CIFormRaw
	case "minio":
		return model.CIFormAKSK
	default:
		return model.CIFormBasic
	}
}

func (s *Service) Create(ctx context.Context, req CreateReq) (*model.CICredential, error) {
	form := req.Form
	if form == "" {
		form = formFromType(req.Type) // 兼容旧前端
	}
	if !validForms[form] {
		return nil, errcode.Newf(errcode.InvalidParam, "非法凭证 form: %s", form)
	}
	if !credNameRe.MatchString(req.Name) {
		return nil, errcode.Newf(errcode.InvalidParam, "凭证名非法: %s（须为小写字母/数字/-，字母数字开头结尾；会拼进 K8s Secret 名 cred-<name>）", req.Name)
	}

	// 0) 同集群重名先拒：Secret 名由 name 派生（cred-<name>），重名创建会覆写
	//    既有凭证的密钥材料（EnsureSecret 对已存在 Secret 走合并 patch），
	//    之后删除任一条记录会删光共享 Secret，让另一条记录悬空
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&model.CICredential{}).
		Where("cluster_name = ? AND name = ?", req.Cluster, req.Name).Count(&cnt).Error; err != nil {
		return nil, err
	}
	if cnt > 0 {
		return nil, errcode.Newf(errcode.Conflict, "凭证名已存在: %s", req.Name)
	}

	// 1) 先写 K8s Secret（成功才落库，保证引用不悬空）
	secretName := "cred-" + req.Name
	data, err := tekton.BuildCredentialSecretData(form, tekton.SecretFields{
		Host: req.Host, Username: req.Username, Password: req.Password, Token: req.Token,
		Dockerconfig: req.Dockerconfig, Kubeconfig: req.Kubeconfig,
		AccessKey: req.AccessKey, SecretKey: req.SecretKey,
		Region: req.Region, Endpoint: req.Endpoint, Data: req.Data,
	})
	if err != nil {
		return nil, errcode.Newf(errcode.InvalidParam, "%v", err)
	}
	k8s, err := s.k8sFor(req.Cluster)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	if err := s.syncSecret(ctx, k8s, req.Cluster, secretName, data); err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "写入 K8s Secret 失败: %v", err)
	}

	// 2) 组装非敏感元数据 Extra（host/region/endpoint 等结构化）
	extra := buildExtra(req.Host, req.Region, req.Endpoint)

	// 3) 落库（只存引用与元数据）
	cred := &model.CICredential{
		ClusterName: req.Cluster,
		Name:        req.Name,
		Form:        form,
		Type:        req.Type,
		SecretName:  secretName,
		SecretNS:    s.cfg.PlatformNS,
		Extra:       extra,
		ProjectID:   req.ProjectID,
	}
	if err := s.db.WithContext(ctx).Create(cred).Error; err != nil {
		// 回滚：删掉刚建的 Secret，避免悬空
		s.deleteSecretAll(ctx, req.Cluster, secretName)
		if isUnique(err) {
			return nil, errcode.Newf(errcode.Conflict, "凭证名已存在: %s", req.Name)
		}
		return nil, err
	}
	return cred, nil
}

// buildExtra 把非敏感元数据组装为 JSONObject（host/region/endpoint）。
func buildExtra(host, region, endpoint string) model.JSONObject {
	extra := model.JSONObject{}
	if host != "" {
		extra["host"] = host
	}
	if region != "" {
		extra["region"] = region
	}
	if endpoint != "" {
		extra["endpoint"] = endpoint
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func (s *Service) List(ctx context.Context, cluster string, projectID *uint) ([]model.CICredential, error) {
	q := s.db.WithContext(ctx).Where("cluster_name = ?", cluster).Order("id desc")
	if projectID != nil {
		q = q.Where("project_id = ? OR project_id IS NULL", *projectID)
	} else {
		q = q.Where("project_id IS NULL")
	}
	var cs []model.CICredential
	if err := q.Find(&cs).Error; err != nil {
		return nil, err
	}
	// 填充 References（哪些流水线的最新版本引用了该凭证）
	s.fillReferences(ctx, cs)
	return cs, nil
}

// credReferences 扫描指定集群下流水线的最新版本 DSL，返回 凭证引用值 -> 引用它的流水线名列表。
// 键是 DSL 里的原始引用值（凭证名或早期版本的数字 id 字符串）。
// 凭证与 Secret 都是集群隔离的，只扫本集群的流水线——跨集群的同名/同 id 凭证
// 不构成引用（旧实现 versions 查询不带集群过滤，其它集群的引用会混进来，
// 导致删除被误拦且错误信息里出现空流水线名）。
// 查询失败返回 error：删除守卫依赖此结果，DB 错误必须上抛而不是当「无引用」。
func (s *Service) credReferences(ctx context.Context, cluster string) (map[string][]string, error) {
	var pipes []model.CIPipeline
	if err := s.db.WithContext(ctx).Where("cluster_name = ?", cluster).Find(&pipes).Error; err != nil {
		return nil, err
	}
	idSet := map[uint]bool{}
	nameByID := map[uint]string{}
	for i := range pipes {
		idSet[pipes[i].ID] = true
		nameByID[pipes[i].ID] = pipes[i].Name
	}
	var versions []model.CIPipelineVersion
	if err := s.db.WithContext(ctx).
		Where("version = (SELECT MAX(version) FROM ci_pipeline_versions pv2 WHERE pv2.pipeline_id = ci_pipeline_versions.pipeline_id)").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for i := range versions {
		if !idSet[versions[i].PipelineID] {
			continue
		}
		g, err := pipelinesvc.ParseGraph(versions[i].GraphJSON)
		if err != nil {
			continue
		}
		pipe := nameByID[versions[i].PipelineID]
		for _, n := range g.Nodes {
			// 只认 schema 中 type=credential 的属性值（精确匹配凭证名/id），
			// 不再扫全部 string 参数值——任意业务字符串都不该被当成凭证引用
			for _, ref := range credParamValues(s.reg, n) {
				out[ref] = append(out[ref], pipe)
			}
		}
	}
	return out, nil
}

func (s *Service) fillReferences(ctx context.Context, cs []model.CICredential) {
	if len(cs) == 0 {
		return
	}
	// List 按集群过滤，cs 同属一个集群；引用展示失败不影响列表本身
	refs, err := s.credReferences(ctx, cs[0].ClusterName)
	if err != nil {
		return
	}
	for i := range cs {
		// 名字引用 + 早期数字 id 引用都算
		var merged []string
		merged = append(merged, refs[cs[i].Name]...)
		idStr := strconv.FormatUint(uint64(cs[i].ID), 10)
		merged = append(merged, refs[idStr]...)
		if len(merged) > 0 {
			cs[i].References = merged
		}
	}
}

// activeRunRefs 检查进行中的 run（pending/running）是否引用该凭证，返回 run id 列表。
// 删除凭证会连带删 K8s Secret，排队中的 TaskRun 起 Pod 时 envFrom 会失败。
// 按集群过滤：run 与凭证同集群，跨集群同名凭证不相关。
// 查询失败返回 error：DB 抖动时「查不到引用」不能当「无引用」放行删除。
func (s *Service) activeRunRefs(ctx context.Context, cluster, credName, credIDStr string) ([]uint, error) {
	var runs []model.CIRun
	if err := s.db.WithContext(ctx).
		Where("status IN ? AND cluster_name = ?", []string{model.CIRunStatusPending, model.CIRunStatusRunning}, cluster).
		Find(&runs).Error; err != nil {
		return nil, err
	}
	var out []uint
	for i := range runs {
		var v model.CIPipelineVersion
		if err := s.db.WithContext(ctx).First(&v, runs[i].VersionID).Error; err != nil {
			continue
		}
		g, err := pipelinesvc.ParseGraph(v.GraphJSON)
		if err != nil {
			continue
		}
		for _, n := range g.Nodes {
			// 只匹配 schema 中 type=credential 属性名的取值，避免参数值里恰好
			// 出现同名字符串（如分支名/镜像名）造成的假阳性；名字与早期数字 id 都认
			for _, ref := range credParamValues(s.reg, n) {
				if ref == credName || ref == credIDStr {
					out = append(out, runs[i].ID)
					break
				}
			}
		}
	}
	return out, nil
}

// UpdateReq 更新凭证密钥材料。form/type/name 不可改（DSL 按 name 引用，改名会产生孤儿引用）。
// 各字段 nil 或空串 = 保持原值（password/token 留空即可只改 host/username）。
type UpdateReq struct {
	Host         *string `json:"host"`
	Username     *string `json:"username"`
	Password     *string `json:"password"`
	Token        *string `json:"token"`
	Dockerconfig *string `json:"dockerconfig"`
	Kubeconfig   *string `json:"kubeconfig"`
	AccessKey    *string `json:"accessKey"`
	SecretKey    *string `json:"secretKey"`
	Region       *string `json:"region"`
	Endpoint     *string `json:"endpoint"`
	Data         map[string]string `json:"data"`
}

// Update 更新凭证密钥材料：读旧 Secret 取原值（空字段保持）→ 重建 K8s Secret → 同步 Extra。
func (s *Service) Update(ctx context.Context, id uint, req UpdateReq) (*model.CICredential, error) {
	var c model.CICredential
	if err := s.db.WithContext(ctx).First(&c, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "凭证不存在")
		}
		return nil, err
	}
	// 读旧 Secret 取原值（空字段保持）。读失败不能静默当作空 map：
	// 那样仅改 host/region 的请求会用空密钥重建 Secret，凭证材料被静默抹掉。
	// Secret 不存在（被外部删除）视为空，允许从请求重建。
	k8s, err := s.k8sFor(c.ClusterName)
	if err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	old, err := k8s.SecretData(ctx, c.SecretNS, c.SecretName)
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, errcode.Newf(errcode.DepUnavailable, "读取原凭证 Secret 失败，为防密钥材料丢失已拒绝更新: %v", err)
	}
	str := func(m map[string][]byte, k string) string { return string(m[k]) }
	host := extraStr(c.Extra, "host")
	region := extraStr(c.Extra, "region")
	endpoint := extraStr(c.Extra, "endpoint")
	f := tekton.SecretFields{
		Host: host, Region: region, Endpoint: endpoint,
		Username:     str(old, tekton.SecretKeyUsername),
		Password:     str(old, tekton.SecretKeyPassword),
		Token:        str(old, tekton.SecretKeyToken),
		Dockerconfig: str(old, tekton.SecretKeyDockerconfig),
		Kubeconfig:   str(old, tekton.SecretKeyKubeconfig),
		AccessKey:    str(old, tekton.SecretKeyAccessKey),
		SecretKey:    str(old, tekton.SecretKeySecretKey),
	}
	// 原始 raw data 也带回（raw form 重建需保留未改 key）
	if c.Form == model.CIFormRaw {
		f.Data = secretDataToMap(old)
	}
	// 用请求覆盖非空字段
	if req.Host != nil {
		host = *req.Host
		f.Host = host
	}
	if req.Username != nil {
		f.Username = *req.Username
	}
	if req.Password != nil && *req.Password != "" {
		f.Password = *req.Password
	}
	if req.Token != nil && *req.Token != "" {
		f.Token = *req.Token
	}
	if req.Dockerconfig != nil && *req.Dockerconfig != "" {
		f.Dockerconfig = *req.Dockerconfig
	}
	if req.Kubeconfig != nil && *req.Kubeconfig != "" {
		f.Kubeconfig = *req.Kubeconfig
	}
	if req.AccessKey != nil && *req.AccessKey != "" {
		f.AccessKey = *req.AccessKey
	}
	if req.SecretKey != nil && *req.SecretKey != "" {
		f.SecretKey = *req.SecretKey
	}
	if req.Region != nil {
		region = *req.Region
		f.Region = region
	}
	if req.Endpoint != nil {
		endpoint = *req.Endpoint
		f.Endpoint = endpoint
	}
	if req.Data != nil && c.Form == model.CIFormRaw {
		f.Data = req.Data
	}
	data, err := tekton.BuildCredentialSecretData(c.Form, f)
	if err != nil {
		return nil, errcode.Newf(errcode.InvalidParam, "%v", err)
	}
	if err := s.syncSecret(ctx, k8s, c.ClusterName, c.SecretName, data); err != nil {
		return nil, errcode.Newf(errcode.DepUnavailable, "更新 K8s Secret 失败: %v", err)
	}
	// 同步 Extra（host/region/endpoint 变化时）
	newExtra := buildExtra(host, region, endpoint)
	if !extraEqual(c.Extra, newExtra) {
		if err := s.db.WithContext(ctx).Model(&c).Update("extra", newExtra).Error; err != nil {
			return nil, err
		}
		c.Extra = newExtra
		c.UpdatedAt = time.Now()
	}
	return &c, nil
}

// extraStr 从 JSONObject 取字符串字段。
func extraStr(m model.JSONObject, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// extraEqual 比较两个 JSONObject 是否等价（nil 与空 map 视为相等）。
func extraEqual(a, b model.JSONObject) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || fmt.Sprint(va) != fmt.Sprint(vb) {
			return false
		}
	}
	return true
}

// secretDataToMap 把 Secret data（[]byte 值）转为 string map（raw form 重建用）。
func secretDataToMap(m map[string][]byte) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

func (s *Service) Delete(ctx context.Context, id uint) error {
	var c model.CICredential
	if err := s.db.WithContext(ctx).First(&c, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errcode.New(errcode.NotFound, "凭证不存在")
		}
		return err
	}
	credIDStr := strconv.FormatUint(uint64(c.ID), 10)
	// 0) 进行中的 run 仍引用该凭证时拒绝删除（删 Secret 会让排队 TaskRun 起 Pod 失败）
	refs, err := s.activeRunRefs(ctx, c.ClusterName, c.Name, credIDStr)
	if err != nil {
		return errcode.Newf(errcode.Internal, "检查运行中引用失败，为避免误删已拒绝: %v", err)
	}
	if len(refs) > 0 {
		return errcode.Newf(errcode.Conflict, "凭证被进行中的执行引用（run #%v），请等待其结束后再删除", refs)
	}
	// 0.5) 最新版本流水线仍引用时拒绝删除：删掉后流水线的 credential 引用悬空，
	// 运行时注入会失败，表现为任务里"未注入 registry 凭证"之类莫名报错
	// （文档承诺的"被引用拒绝"行为）；名字引用与早期数字 id 引用都算
	refsMap, err := s.credReferences(ctx, c.ClusterName)
	if err != nil {
		return errcode.Newf(errcode.Internal, "检查流水线引用失败，为避免误删已拒绝: %v", err)
	}
	if pipes := append(append([]string{}, refsMap[c.Name]...), refsMap[credIDStr]...); len(pipes) > 0 {
		return errcode.Newf(errcode.Conflict, "凭证被流水线 %v 的最新版本引用，请先在设计器中改选其它凭证或移除引用后再删除", pipes)
	}
	// 1) 删库  2) 删 K8s Secret（best effort）
	if err := s.db.WithContext(ctx).Delete(&c).Error; err != nil {
		return err
	}
	s.deleteSecretAll(ctx, c.ClusterName, c.SecretName)
	return nil
}

// GetRef 按集群+名称取凭证引用（供 runtime 注入 Task secretKeyRef）。
// Secret 与凭证都是集群隔离的：跨集群同名凭证不可用（其 Secret 不在本集群）。
func (s *Service) GetRef(ctx context.Context, cluster, name string) (*model.CICredential, error) {
	var c model.CICredential
	if err := s.db.WithContext(ctx).Where("cluster_name = ? AND name = ?", cluster, name).First(&c).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.Newf(errcode.NotFound, "凭证不存在: %s", name)
		}
		return nil, err
	}
	return &c, nil
}

// GetByID 按 id 取凭证（handler 鉴权用：更新/删除前先取归属项目校验权限）。
func (s *Service) GetByID(ctx context.Context, id uint) (*model.CICredential, error) {
	var c model.CICredential
	if err := s.db.WithContext(ctx).First(&c, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.New(errcode.NotFound, "凭证不存在")
		}
		return nil, err
	}
	return &c, nil
}

// GitAuth 返回凭证的 git 明文访问凭据（设计器查仓库 refs 等 server 侧直连 git 场景用）。
// basic → username/password；token → token；raw → 可能自带上述任一键。
// 其余 form（registry/kubeconfig/aksk）不适用 git 访问，返回明确错误。
func (s *Service) GitAuth(ctx context.Context, c *model.CICredential) (username, password, token string, err error) {
	k8s, err := s.k8sFor(c.ClusterName)
	if err != nil {
		return "", "", "", errcode.Newf(errcode.DepUnavailable, "集群不可用: %v", err)
	}
	data, err := k8s.SecretData(ctx, c.SecretNS, c.SecretName)
	if err != nil {
		return "", "", "", errcode.Newf(errcode.DepUnavailable, "读取凭证 Secret 失败: %v", err)
	}
	str := func(k string) string { return string(data[k]) }
	switch c.Form {
	case model.CIFormBasic:
		return str(tekton.SecretKeyUsername), str(tekton.SecretKeyPassword), "", nil
	case model.CIFormToken:
		return "", "", str(tekton.SecretKeyToken), nil
	case model.CIFormRaw:
		return str(tekton.SecretKeyUsername), str(tekton.SecretKeyPassword), str(tekton.SecretKeyToken), nil
	default:
		return "", "", "", errcode.Newf(errcode.InvalidParam, "凭证 form %s 不支持 git 访问", c.Form)
	}
}

// GetRefByID 按集群+id 取凭证引用（兼容前端早期提交数字 id 的 DSL）。
func (s *Service) GetRefByID(ctx context.Context, cluster string, id uint) (*model.CICredential, error) {
	var c model.CICredential
	if err := s.db.WithContext(ctx).Where("cluster_name = ?", cluster).First(&c, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errcode.Newf(errcode.NotFound, "凭证不存在: id=%d", id)
		}
		return nil, err
	}
	return &c, nil
}

// isUnique 唯一约束冲突判断（Postgres SQLSTATE 23505，见 errcode.IsUniqueViolation）。
func isUnique(err error) bool {
	return errcode.IsUniqueViolation(err)
}
