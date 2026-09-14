package node

import (
	"io/fs"
	"testing"
)

// 全局变量 imageRegistry 必须替换节点模板里的镜像仓库前缀，
// 且优先于节点参数（流水线图里误填同名参数不能把镜像仓库改坏）。
func TestRenderTaskImageRegistryGlobal(t *testing.T) {
	fsys, err := fs.Sub(NodesFS, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := NewLoader(reg).WithGlobals(map[string]interface{}{
		"imageRegistry": "my.harbor:5000/base",
	}).LoadEmbedded(fsys); err != nil {
		t.Fatal(err)
	}

	n, ok := reg.Get("git-clone")
	if !ok {
		t.Fatal("git-clone 节点未注册")
	}
	spec, err := n.RenderTask(map[string]interface{}{
		// 同名节点参数必须被全局变量压过
		"imageRegistry": "evil.registry/bad",
	})
	if err != nil {
		t.Fatalf("RenderTask: %v", err)
	}
	if len(spec.Steps) == 0 {
		t.Fatal("git-clone 无 step")
	}
	want := "my.harbor:5000/base/git:2.43.4"
	if got := spec.Steps[0].Image; got != want {
		t.Fatalf("镜像 = %q, 想 %q", got, want)
	}
}
