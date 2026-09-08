package tekton

import "testing"

func TestValidSecretKey(t *testing.T) {
	long := make([]rune, 64)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct {
		key string
		ok  bool
	}{
		{"cosign_pub", true},
		{"my-token", true},
		{"a.b", true},
		{"9lives", true},
		{"a", true},
		{"", false},
		{".dockerconfigjson", false},
		{".hidden", false},
		{"-leading", false},
		{"UPPER", false},
		{"under_score", true}, // K8s Secret key 允许下划线
		{"space key", false},
		{string(long), false},
		{string(long[:63]), true},
	}
	for _, c := range cases {
		if got := ValidSecretKey(c.key); got != c.ok {
			t.Errorf("ValidSecretKey(%q) = %v, want %v", c.key, got, c.ok)
		}
	}
}
