package tekton

import "testing"

// mapCondition：when 未命中的 TaskRun（reason=WhenExpressionsEvaluatedFalse，
// kubectl 显示 SKIPPED）必须映射为 skipped，否则节点在前端永远不收敛。
func TestMapConditionSkipped(t *testing.T) {
	cases := []struct {
		status, reason, want string
	}{
		{"True", "WhenExpressionsEvaluatedFalse", "skipped"},
		{"Unknown", "WhenExpressionsEvaluatedFalse", "skipped"},
		{"True", "Succeeded", "success"},
		{"False", "Failed", "failed"},
		{"Unknown", "Running", "running"},
		{"False", "TaskRunCancelled", "cancelled"},
		{"False", "PipelineRunCancelled", "cancelled"},
		{"Unknown", "PipelineRunCancelled", "cancelled"},
	}
	for _, c := range cases {
		if got := mapCondition(c.status, c.reason); got != c.want {
			t.Errorf("mapCondition(%q, %q) = %q, want %q", c.status, c.reason, got, c.want)
		}
	}
}
