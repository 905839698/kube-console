// registrar.go 根据 TaskRun 的 results + 节点参数生成制品记录。
//
// 产出来源（节点 type → 制品）：
//   - build-image / push-registry → 容器镜像（Harbor），imageRef / imageDigest
//   - upload-artifact            → 文件制品（Nexus/MinIO），storage_path / sha256 / version
//
// 其余节点不直接产制品（编译产物需经 upload-artifact 节点上传才登记）。
package artifact

import (
	"context"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"kube-console/server/internal/ci/errcode"
	"kube-console/server/internal/model"
)

// Registrar 制品注册器。
type Registrar struct {
	db *gorm.DB
}

func NewRegistrar(db *gorm.DB) *Registrar { return &Registrar{db: db} }

// RunCtx 注册上下文（来自 run）。
type RunCtx struct {
	ClusterName   string
	ProjectID     uint
	PipelineID    uint
	PipelineRunID uint
}

// FromTaskRun 计算某 TaskRun 成功后应登记的制品（不落库）。
func (r *Registrar) FromTaskRun(nodeType string, params map[string]interface{}, results map[string]string, ctx RunCtx) []model.CIArtifact {
	switch nodeType {
	case "build-image", "push-registry":
		return imageArtifacts(results, ctx)
	case "upload-artifact":
		return fileArtifacts(params, results, ctx)
	default:
		return nil
	}
}

// Register 落库。复合唯一冲突 = 重复登记，视为已存在直接吞掉。
func (r *Registrar) Register(ctx context.Context, items []model.CIArtifact) error {
	if len(items) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).Create(&items).Error
	if err != nil && errcode.IsUniqueViolation(err) {
		return nil
	}
	return err
}

func imageArtifacts(results map[string]string, ctx RunCtx) []model.CIArtifact {
	ref := results["imageRef"]
	if ref == "" {
		return nil
	}
	name, version := splitImageRef(ref)
	return []model.CIArtifact{{
		ClusterName:   ctx.ClusterName,
		ProjectID:     ctx.ProjectID,
		PipelineID:    ctx.PipelineID,
		PipelineRunID: ctx.PipelineRunID,
		Name:          name,
		Type:          model.CIArtImage,
		Version:       version,
		StorageType:   model.CIStoHarbor,
		StoragePath:   ref,
		Digest:        results["imageDigest"],
	}}
}

func fileArtifacts(params map[string]interface{}, results map[string]string, ctx RunCtx) []model.CIArtifact {
	p := results["storage_path"]
	if p == "" {
		return nil
	}
	artType := strParam(params, "type", model.CIArtGeneric)
	storage := strParam(params, "storage", model.CIStoNexus)
	version := results["artifact_version"]
	if version == "" {
		version = strParam(params, "version", "")
	}
	return []model.CIArtifact{{
		ClusterName:   ctx.ClusterName,
		ProjectID:     ctx.ProjectID,
		PipelineID:    ctx.PipelineID,
		PipelineRunID: ctx.PipelineRunID,
		Name:          baseName(p),
		Type:          artType,
		Version:       version,
		StorageType:   storage,
		StoragePath:   p,
		SHA256:        results["sha256"],
		Size:          strToInt64(results["size"]),
	}}
}

func splitImageRef(ref string) (name, version string) {
	// ref 形如 harbor.example.com/project/app:tag 或 .../app@sha256:xxx
	rest := ref
	if at := strings.LastIndex(rest, "@"); at > 0 {
		// digest 形态：version 取整个 @sha256:xxx，name 从 @ 之前截取
		version = rest[at+1:]
		rest = rest[:at]
	} else if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		// tag 形态：先剥掉 :tag 再取末段，否则 name 会带上 tag（与 digest 形态不一致）
		version = rest[i+1:]
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		name = rest[i+1:]
	} else {
		name = rest
	}
	return name, version
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func strParam(params map[string]interface{}, key, def string) string {
	if v, ok := params[key].(string); ok && v != "" {
		return v
	}
	return def
}

func strToInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
