package node

import (
	"io/fs"
	"testing"
)

func TestLoadEmbeddedAll(t *testing.T) {
	reg := NewRegistry()
	sub, err := fs.Sub(NodesFS, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewLoader(reg).LoadEmbedded(sub); err != nil {
		t.Fatal(err)
	}
	list := reg.List()
	if len(list) != 19 {
		t.Fatalf("expected 19 nodes, got %d", len(list))
	}
	if _, ok := reg.Get("git-clone"); !ok {
		t.Fatal("git-clone missing")
	}
	if _, ok := reg.Get("harbor-scan"); !ok {
		t.Fatal("harbor-scan missing")
	}
}
