package node

import (
	"sync"

	"kube-console/server/internal/ci/nodetype"
)

// Registry 是 nodetype.Registry 的内存实现（线程安全）。
// 启动时由 Loader 填充；运行期只读。
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]nodetype.Node
	order []string
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{nodes: make(map[string]nodetype.Node)}
}

func (r *Registry) Register(n nodetype.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta := n.Meta()
	if _, ok := r.nodes[meta.Type]; !ok {
		r.order = append(r.order, meta.Type)
	}
	r.nodes[meta.Type] = n
}

func (r *Registry) Get(nodeType string) (nodetype.Node, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[nodeType]
	return n, ok
}

func (r *Registry) List() []nodetype.Meta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]nodetype.Meta, 0, len(r.order))
	for _, t := range r.order {
		out = append(out, r.nodes[t].Meta())
	}
	return out
}
