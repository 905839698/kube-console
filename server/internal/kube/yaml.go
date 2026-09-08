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

// ApplyYAML 创建或更新资源（create-or-update），返回是否新建
func ApplyYAML(ctx context.Context, dyn dynamic.Interface, yamlStr string) (created bool, err error) {
	obj, gvr, err := parseYAML(yamlStr)
	if err != nil {
		return false, err
	}
	ns := obj.GetNamespace()
	if ns == "" {
		ns = "default"
		obj.SetNamespace(ns)
	}
	_, err = dyn.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !errors.IsAlreadyExists(err) {
		return false, err
	}
	// 已存在则更新（保留调用方 YAML 中的 resourceVersion 做乐观锁；缺失时从现有对象获取）
	if obj.GetResourceVersion() == "" {
		if existing, gerr := dyn.Resource(gvr).Namespace(ns).Get(ctx, obj.GetName(), metav1.GetOptions{}); gerr == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
		}
	}
	_, err = dyn.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return false, err
	}
	return false, nil
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
