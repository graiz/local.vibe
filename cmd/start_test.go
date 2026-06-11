package cmd

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestSplitAtDash(t *testing.T) {
	// Mirrors what cobra hands RunE: the literal "--" is already removed from
	// args, and its position arrives separately via ArgsLenAtDash() (dashPos).
	tests := []struct {
		name       string
		args       []string
		dashPos    int
		wantBefore []string
		wantAfter  []string
	}{
		{"no args", nil, -1, nil, nil},
		{"name only, no dash", []string{"myapp"}, -1, []string{"myapp"}, nil},
		{"name+port+cmd", []string{"myapp", "3000", "npm", "run", "dev"}, 2,
			[]string{"myapp", "3000"}, []string{"npm", "run", "dev"}},
		{"dash first", []string{"echo", "hi"}, 0,
			[]string{}, []string{"echo", "hi"}},
		{"dashPos out of range is ignored", []string{"a"}, 9, []string{"a"}, nil},
	}
	for _, tt := range tests {
		before, after := splitAtDash(tt.args, tt.dashPos)
		if !reflect.DeepEqual(before, tt.wantBefore) {
			t.Errorf("%s: before = %v; want %v", tt.name, before, tt.wantBefore)
		}
		if !reflect.DeepEqual(after, tt.wantAfter) {
			t.Errorf("%s: after = %v; want %v", tt.name, after, tt.wantAfter)
		}
	}
}

// TestStartCommandDashParsing is the regression test for the bug where
// `vibe start <name> <port> -- <cmd>` always failed with a usage error.
// With flag parsing enabled, cobra strips the literal "--" before RunE runs,
// so the old splitAtDash — which scanned args for a "--" token — never found
// it and every invocation fell through to the usage error. This drives a
// command through cobra's real argument handling to prove the split is
// recovered from ArgsLenAtDash(). A pure splitAtDash unit test can't catch
// this regression because it never goes through the cobra layer.
func TestStartCommandDashParsing(t *testing.T) {
	var gotBefore, gotAfter []string
	cmd := &cobra.Command{
		Use:          "start",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			gotBefore, gotAfter = splitAtDash(args, c.ArgsLenAtDash())
			return nil
		},
	}
	cmd.SetArgs([]string{"myapp", "3000", "--", "npm", "run", "dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	wantBefore := []string{"myapp", "3000"}
	wantAfter := []string{"npm", "run", "dev"}
	if !reflect.DeepEqual(gotBefore, wantBefore) {
		t.Errorf("before = %v; want %v", gotBefore, wantBefore)
	}
	if !reflect.DeepEqual(gotAfter, wantAfter) {
		t.Errorf("after = %v; want %v", gotAfter, wantAfter)
	}
}
