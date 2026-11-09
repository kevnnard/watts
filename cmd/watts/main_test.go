package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"--help"}, 0, "import-consumo", ""},
		{[]string{"-h"}, 0, "config.toml", ""},
		{[]string{"help"}, 0, "Commands:", ""},
		{[]string{"version"}, 0, "watts dev\n", ""},
		{[]string{"--version"}, 0, "watts dev\n", ""},
		{[]string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		code := run(tt.args, &out, &errb)
		if code != tt.code || !strings.Contains(out.String(), tt.stdout) || !strings.Contains(errb.String(), tt.stderr) {
			t.Errorf("run(%q) = %d, stdout %q, stderr %q", tt.args, code, out.String(), errb.String())
		}
	}
}
