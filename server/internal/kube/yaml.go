package kube

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// GetYAML 获取指定资源的原始 YAML
func GetYAML(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) (string, error) {
	obj, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	// 精简输出：移除 metadata 中的托管字段（uid/resourceVersion 等保留 resourceVersion 以便更新）
	cleanManagedFields(obj)
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ApplyYAML 创建或更新资源（先查再改），返回是否新建。
// 不能"先 Create、AlreadyExists 再转 Update"：Service 这类带分配字段的资源，
// Create 会先做 ClusterIP 分配校验（"provided IP is already allocated"）再做同名冲突检查，
// 对已存在的 ClusterIP Service 重新 Apply 会在分配阶段直接报 422，永远到不了更新分支。
func ApplyYAML(ctx context.Context, dyn dynamic.Interface, yamlStr string) (created bool, err error) {
	obj, gvr, err := parseYAML(yamlStr)
	if err != nil {
		return false, err
	}
	if obj.GetName() == "" {
		return false, errors.NewBadRequest("YAML 缺少 metadata.name")
	}
	ns := obj.GetNamespace()
	if ns == "" {
		ns = "default"
		obj.SetNamespace(ns)
	}
	ri := dyn.Resource(gvr).Namespace(ns)

	// 已存在 → 直接更新：取现有对象最新 resourceVersion 做乐观锁
	existing, gerr := ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if gerr == nil {
		obj.SetResourceVersion(existing.GetResourceVersion())
		if _, err = ri.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return false, err
		}
		return false, nil
	}
	if !errors.IsNotFound(gerr) {
		return false, gerr
	}

	// 不存在 → 创建（Create 不允许携带 resourceVersion，K8s 会拒绝）
	obj.SetResourceVersion("")
	_, err = ri.Create(ctx, obj, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !errors.IsAlreadyExists(err) {
		return false, err
	}
	// 并发竞争：查询后被他人创建 → 转更新
	if existing, gerr = ri.Get(ctx, obj.GetName(), metav1.GetOptions{}); gerr != nil {
		return false, gerr
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = ri.Update(ctx, obj, metav1.UpdateOptions{})
	return false, err
}

// parseYAML 将 YAML 字符串解析为 unstructured 对象，并根据 kind 推导 GVR
func parseYAML(yamlStr string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &m); err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	obj := &unstructured.Unstructured{Object: m}
	kind := obj.GetKind()
	gvk := obj.GroupVersionKind()
	if kind == "" || gvk.Version == "" {
		return nil, schema.GroupVersionResource{}, errors.NewBadRequest("YAML 缺少 apiVersion/kind")
	}
	gvr := schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: plural(gvk.Kind),
	}
	return obj, gvr, nil
}

// plural 根据 kind 推导资源复数名（已知 kind 走 KindMap，未知按英文复数规则兜底）
func plural(kind string) string {
	if gvr, ok := KindMap[kind]; ok {
		return gvr.Resource
	}
	k := strings.ToLower(kind)
	// 优先尝试直接加 s（如 "Gateway" → "gateways"），命中 KindMap 即返回
	if _, ok := KindMap[k+"s"]; ok {
		return k + "s"
	}
	switch {
	case strings.HasSuffix(k, "s"), strings.HasSuffix(k, "x"), strings.HasSuffix(k, "ch"), strings.HasSuffix(k, "sh"):
		return k + "es"
	case strings.HasSuffix(k, "y") && len(k) > 1:
		return strings.TrimSuffix(k, "y") + "ies"
	default:
		return k + "s"
	}
}

// cleanManagedFields 移除 status 与 metadata.managedFields，避免输出冗余字段
func cleanManagedFields(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "generation")
}

// CleanForExport 导出用深度清洗：在 cleanManagedFields 基础上再去 resourceVersion、status 与
// kubectl last-applied 注解，导出的 YAML 可直接重新导入（apply）
func CleanForExport(obj *unstructured.Unstructured) {
	cleanManagedFields(obj)
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "status")
	const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
	if ann := obj.GetAnnotations(); ann != nil {
		if _, ok := ann[lastApplied]; ok {
			delete(ann, lastApplied)
			if len(ann) == 0 {
				unstructured.RemoveNestedField(obj.Object, "metadata", "annotations")
			} else {
				obj.SetAnnotations(ann)
			}
		}
	}
}
