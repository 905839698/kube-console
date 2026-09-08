package runtime

import (
	"context"
	"log"
	"strconv"
	"strings"

	"kube-console/server/internal/model"
	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/compiler"
	dsl "kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/tekton"
)

// enrichGraph 编译前对 DSL 做运行时增强：
//   - upload-artifact 节点注入存储端点伪参数（nexusBase/minioEndpoint/minioBucket）
//   - build-image 节点注入 BuildKit 构建器地址（buildkitdAddr）
//   - branchOverride 非空时覆盖 git-clone 节点的 branch（Webhook 按实际推送分支）
func (s *Service) enrichGraph(g *dsl.Graph, branchOverride string) {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		switch n.Type {
		case "upload-artifact":
			if n.Params == nil {
				n.Params = map[string]interface{}{}
			}
			if _, ok := n.Params["nexusBase"]; !ok {
				n.Params["nexusBase"] = s.cfg.NexusURL
			}
			if _, ok := n.Params["minioEndpoint"]; !ok {
				n.Params["minioEndpoint"] = s.cfg.MinIOEndpoint
			}
			if _, ok := n.Params["minioBucket"]; !ok {
				n.Params["minioBucket"] = s.cfg.MinIOBucket
			}
		case "build-image":
			// BuildKit 构建器地址（buildctl --addr），用户可在节点上覆盖
			if n.Params == nil {
				n.Params = map[string]interface{}{}
			}
			if v, ok := n.Params["buildkitdAddr"].(string); !ok || v == "" {
				n.Params["buildkitdAddr"] = s.cfg.BuildkitAddr
			}
		case "git-clone":
			if branchOverride != "" {
				if n.Params == nil {
					n.Params = map[string]interface{}{}
				}
				n.Params["branch"] = branchOverride
				// 分支触发时清掉 tag/commit，避免歧义
				delete(n.Params, "tag")
				delete(n.Params, "commit")
			}
		}
	}
}

// injectCredentials 给声明了 credential 参数的任务注入凭证，按 form（密钥形态）分流：
//
//   - dockerconfig：收集为 PipelineRun 的 podTemplate.imagePullSecrets（镜像拉取）；
//     任务内 push 挂载 .dockerconfigjson 到 /ci/cred/<secret>/config.json，buildah/cosign
//     经 DOCKER_CONFIG 自动读，无需 env 明文。
//   - kubeconfig：挂载 kubeconfig 到 /ci/cred/<secret>/config + env KUBECONFIG，
//     kubectl/helm 自动读，部署到外部目标集群。
//
// 挂载点不能用 /tekton/ 前缀：Tekton admission webhook
// (validation.webhook.pipeline.tekton.dev) 拒绝挂载到其保留目录下的 volumeMount。
//   - basic/token/aksk/raw：envFrom secretRef，且只挂到「脚本里实际引用了该 Secret key」
//     的 step（按 $key / env://key 引用判断），而非任务内所有 step 全量可见。
//     aksk 的 AWS 标准名（AWS_ACCESS_KEY_ID 等）已双写进 Secret，云 SDK 经 envFrom 直接读。
//
// 凭证名 → K8s Secret 名由 credential 服务解析；解析失败仅告警
// （任务内会因缺凭证失败，日志可见）。返回本 run 需要的 imagePullSecrets 列表。
// pipelineProjectID 为所属项目：项目级凭证只允许所属项目的流水线引用，
// 否则 A 项目开发者按名引用 B 项目凭证即可把 B 的密钥注入自己的任务。
func (s *Service) injectCredentials(ctx context.Context, spec *compiler.PipelineSpec, g *dsl.Graph, pipelineProjectID uint) []string {
	// 泛化：扫描节点 schema 中所有 credential 类型参数（如 credential / token），
	// 而不写死某个参数名。
	credByNode := map[string]string{}
	reg := s.pipe.Compiler.Nodes
	for _, n := range g.Nodes {
		if n.Params == nil {
			continue
		}
		plugin, ok := reg.Get(n.Type)
		if !ok {
			continue
		}
		for _, p := range plugin.Meta().Properties {
			if p.Type != "credential" {
				continue
			}
			// 凭证参数值：string（凭证名，标准）或 number（前端早期提交的 id，兼容）
			switch v := n.Params[p.Name].(type) {
			case string:
				if v != "" {
					credByNode[n.ID] = v
				}
			case float64:
				credByNode[n.ID] = strconv.FormatInt(int64(v), 10)
			}
			if credByNode[n.ID] != "" {
				break
			}
		}
	}
	if len(credByNode) == 0 || s.creds == nil {
		return nil
	}
	var pullSecrets []string
	for i := range spec.Spec.Tasks {
		task := &spec.Spec.Tasks[i]
		credName, ok := credByNode[task.Name]
		if !ok || task.TaskSpec == nil {
			continue
		}
		cred, err := s.creds.GetRef(ctx, credName)
		if err != nil {
			// 兼容数字 id 形式的引用（前端早期版本提交凭证 id）
			if id, perr := strconv.ParseUint(credName, 10, 64); perr == nil {
				cred, err = s.creds.GetRefByID(ctx, uint(id))
			}
		}
		if err != nil {
			log.Printf("runtime: 凭证 %s 解析失败，任务 %s 将无法使用凭证", credName, task.Name)
			continue
		}
		// 归属校验：项目级凭证不允许跨项目引用（平台级 nil 凭证全局可用）
		if cred.ProjectID != nil && *cred.ProjectID != pipelineProjectID {
			log.Printf("runtime: 凭证 %s 属于其它项目，任务 %s 拒绝注入", credName, task.Name)
			continue
		}
		switch cred.Form {
		case model.CIFormDockerconfig:
			// 镜像拉取走 imagePullSecrets；任务内 push 挂载 .dockerconfigjson，
			// buildah/cosign 经 DOCKER_CONFIG 自动读，无需 username/password env 明文
			pullSecrets = appendUniqueString(pullSecrets, cred.SecretName)
			mountCredentialFile(task, cred.SecretName, tekton.SecretKeyDockerconfig,
				"cred-"+cred.SecretName, "/ci/cred/"+cred.SecretName, "config.json",
				"DOCKER_CONFIG", "/ci/cred/"+cred.SecretName)
		case model.CIFormKubeconfig:
			// 挂载 kubeconfig 文件 + env KUBECONFIG，kubectl/helm 自动读（部署到外部目标集群）
			mountCredentialFile(task, cred.SecretName, tekton.SecretKeyKubeconfig,
				"cred-"+cred.SecretName, "/ci/cred/"+cred.SecretName, "config",
				"KUBECONFIG", "/ci/cred/"+cred.SecretName+"/config")
		default:
			// basic/token/aksk/raw → envFrom，只挂引用了凭证 key 的 step
			keys := s.credentialKeys(ctx, cred)
			matched := false
			for j := range task.TaskSpec.Steps {
				if stepUsesSecretKeys(task.TaskSpec.Steps[j], keys) {
					task.TaskSpec.Steps[j].EnvFrom = append(
						task.TaskSpec.Steps[j].EnvFrom,
						nodetype.StepEnvFrom{SecretRef: &nodetype.SecretRef{Name: cred.SecretName}},
					)
					matched = true
				}
			}
			if !matched {
				// 兜底：模板未在脚本文本中显式引用 key（如经其它间接方式使用），
				// 保持旧行为全 step 注入并告警，避免静默弄丢凭证
				log.Printf("runtime: 任务 %s 的 step 均未显式引用凭证 %s 的 key，降级为全 step 注入", task.Name, credName)
				for j := range task.TaskSpec.Steps {
					task.TaskSpec.Steps[j].EnvFrom = append(
						task.TaskSpec.Steps[j].EnvFrom,
						nodetype.StepEnvFrom{SecretRef: &nodetype.SecretRef{Name: cred.SecretName}},
					)
				}
			}
		}
	}
	return pullSecrets
}

// mountCredentialFile 给 Task 的所有 step 挂载凭证 Secret 的指定 key 为文件，
// 并注入指向该文件的环境变量。dockerconfig/kubeconfig form 用：
//   - dockerconfig：挂 .dockerconfigjson → config.json，env DOCKER_CONFIG=挂载目录
//   - kubeconfig：挂 kubeconfig → config，env KUBECONFIG=挂载文件路径
//
// 文件挂载不泄漏明文 env（仅暴露路径），比 envFrom 更安全。Volume 在 Task 级声明一次。
func mountCredentialFile(task *compiler.TektonPTask, secretName, key, volName, mountPath, fileName, envName, envValue string) {
	if task.TaskSpec == nil {
		return
	}
	// Task 级 Volume 去重
	hasVol := false
	for _, v := range task.TaskSpec.Volumes {
		if v.Name == volName {
			hasVol = true
			break
		}
	}
	if !hasVol {
		task.TaskSpec.Volumes = append(task.TaskSpec.Volumes, nodetype.Volume{
			Name: volName,
			Secret: &nodetype.SecretVolumeSource{
				SecretName: secretName,
				Items:      []nodetype.KeyToPath{{Key: key, Path: fileName}},
			},
		})
	}
	// 给所有 step 加 VolumeMount + env（文件挂载型凭证无明文泄漏风险，全 step 可见可接受）
	for j := range task.TaskSpec.Steps {
		step := &task.TaskSpec.Steps[j]
		hasMount := false
		for _, vm := range step.VolumeMounts {
			if vm.Name == volName {
				hasMount = true
				break
			}
		}
		if !hasMount {
			step.VolumeMounts = append(step.VolumeMounts, nodetype.VolumeMount{
				Name: volName, MountPath: mountPath, ReadOnly: true,
			})
		}
		hasEnv := false
		for _, e := range step.Env {
			if e.Name == envName {
				hasEnv = true
				break
			}
		}
		if !hasEnv {
			step.Env = append(step.Env, nodetype.EnvVar{Name: envName, Value: envValue})
		}
	}
}

// credentialKeys 取凭证 Secret 的 data key（判断 step 引用用）；
// 读取失败时退回平台约定的标准 key。
func (s *Service) credentialKeys(ctx context.Context, cred *model.CICredential) []string {
	k8s, err := s.k8sFor(cred.ClusterName)
	if err != nil {
		return []string{tekton.SecretKeyUsername, tekton.SecretKeyPassword, tekton.SecretKeyToken}
	}
	keys, err := k8s.SecretKeys(ctx, cred.SecretNS, cred.SecretName)
	if err != nil || len(keys) == 0 {
		return []string{tekton.SecretKeyUsername, tekton.SecretKeyPassword, tekton.SecretKeyToken}
	}
	return keys
}

// stepUsesSecretKeys 判断 step 脚本/命令是否引用了任一 Secret key
// （`$key`、`${key}` 或 cosign 风格的 `env://key`）。
func stepUsesSecretKeys(step nodetype.TektonStep, keys []string) bool {
	text := step.Script
	for _, a := range step.Args {
		text += " " + a
	}
	for _, c := range step.Command {
		text += " " + c
	}
	for _, k := range keys {
		if strings.Contains(text, "$"+k) || strings.Contains(text, "env://"+k) {
			return true
		}
	}
	return false
}

func appendUniqueString(ss []string, v string) []string {
	for _, x := range ss {
		if x == v {
			return ss
		}
	}
	return append(ss, v)
}

// injectTaskRunEnv 给每个 Task 的每个 step 注入 CI_TASK_RUN 环境变量（= 该 TaskRun 名）。
// Tekton 不会自动注入 TaskRun 名，审批等需要「自发现本 TaskRun」的节点靠它。
// Pipeline 任务的 TaskRun 名约定为 <pipelineRunName>-<taskName>。
func (s *Service) injectTaskRunEnv(spec *compiler.PipelineSpec, runName string) {
	for i := range spec.Spec.Tasks {
		task := &spec.Spec.Tasks[i]
		if task.TaskSpec == nil {
			continue
		}
		trName := runName + "-" + task.Name
		for j := range task.TaskSpec.Steps {
			task.TaskSpec.Steps[j].Env = append(task.TaskSpec.Steps[j].Env,
				nodetype.EnvVar{Name: "CI_TASK_RUN", Value: trName})
		}
	}
}

func (s *Service) projectIDOfPipeline(ctx context.Context, pipelineID uint) (uint, error) {
	var p model.CIPipeline
	if err := s.db.WithContext(ctx).First(&p, pipelineID).Error; err != nil {
		return 0, err
	}
	return p.ProjectID, nil
}
