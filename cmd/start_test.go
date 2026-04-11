package cmd

import (
	"reflect"
	"testing"
)

func TestSplitAtDash(t *testing.T) {
	tests := []struct {
		args       []string
		wantBefore []string
		wantAfter  []string
	}{
		{nil, nil, nil},
		{[]string{"myapp"}, []string{"myapp"}, nil},
		{[]string{"myapp", "3000", "--", "npm", "run", "dev"},
			[]string{"myapp", "3000"}, []string{"npm", "run", "dev"}},
		{[]string{"--", "echo", "hi"},
			[]string{}, []string{"echo", "hi"}},
	}

	for _, tt := range tests {
		before, after := splitAtDash(tt.args)
		if !reflect.DeepEqual(before, tt.wantBefore) {
			t.Errorf("splitAtDash(%v) before = %v; want %v", tt.args, before, tt.wantBefore)
		}
		if !reflect.DeepEqual(after, tt.wantAfter) {
			t.Errorf("splitAtDash(%v) after = %v; want %v", tt.args, after, tt.wantAfter)
		}
	}
}
