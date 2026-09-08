package kube

import (
	"context"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResourceDef API 资源定义（资源浏览器的数据源）
type ResourceDef struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Resource   string   `json:"resource"`
	Kind       string   `json:"kind"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
	ShortNames []string `json:"shortNames,omitempty"`
}

// GroupDef API 分组定义
type GroupDef struct {
	Group     string        `json:"group"`
	Preferred string        `json:"preferred"` // 优先展示的版本
	Resources []ResourceDef `json:"resources"`
}

// ListResourceDefs 通过 discovery 列出集群全部 API 资源（按 group 分组，过滤系统内部组）
func ListResourceDefs(ctx context.Context, c *Client) ([]GroupDef, error) {
	_, lists, err := c.Discovery.ServerGroupsAndResources()
	if err != nil {
		// 部分 group 不可用时 discovery 返回 partial 结果 + error
		if len(lists) == 0 {
			return nil, err
		}
	}

	// group -> preferredVersion
	groupPreferred := map[string]string{}
	if groups, gerr := c.Discovery.ServerGroups(); gerr == nil {
		for _, g := range groups.Groups {
			if len(g.Versions) > 0 {
				groupPreferred[g.Name] = g.PreferredVersion.GroupVersion
			}
		}
	}

	defs := make([]GroupDef, 0, len(lists))
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		if isInternalGroup(gv.Group) {
			continue
		}
		group := GroupDef{Group: gv.Group, Preferred: gv.Version}
		if preferred, ok := groupPreferred[gv.Group]; ok {
			group.Preferred = preferred
		}
		for _, r := range list.APIResources {
			// 过滤 subresource（如 deployments/status）
			if strings.Contains(r.Name, "/") {
				continue
			}
			// 只保留可 list 的资源
			if !containsVerb(r.Verbs, "list") {
				continue
			}
			group.Resources = append(group.Resources, ResourceDef{
				Group: gv.Group, Version: gv.Version, Resource: r.Name,
				Kind: r.Kind, Namespaced: r.Namespaced,
				Verbs: r.Verbs, ShortNames: r.ShortNames,
			})
		}
		if len(group.Resources) == 0 {
			continue
		}
		sort.Slice(group.Resources, func(i, j int) bool {
			return group.Resources[i].Resource < group.Resources[j].Resource
		})
		defs = append(defs, group)
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Group < defs[j].Group
	})
	return defs, nil
}

// 过滤系统内部与不可用的组（避免资源浏览器噪点）
// 注意：保留 storage.k8s.io（StorageClass）、policy.k8s.io（PDB）等对用户有意义的组
func isInternalGroup(group string) bool {
	switch group {
	case "apiextensions.k8s.io", "apiregistration.k8s.io", "admissionregistration.k8s.io",
		"authentication.k8s.io", "authorization.k8s.io", "certificates.k8s.io",
		"coordination.k8s.io", "discovery.k8s.io", "events.k8s.io", "flowcontrol.apiserver.k8s.io",
		"metrics.k8s.io", "node.k8s.io", "scheduling.k8s.io":
		return true
	}
	// 内部组（group 为空是核心组，保留）
	return strings.HasPrefix(group, "internal.")
}

func containsVerb(verbs []string, target string) bool {
	for _, v := range verbs {
		if v == target {
			return true
		}
	}
	return false
}
