package main

import (
	"io"
	"strings"
	"testing"
)

func TestRunSampleDM_ArgumentErrors(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	cases := []struct {
		args []string
		want string
	}{
		{nil, "missing kind"},
		{[]string{"--slack-user", "U1"}, "missing kind"},
		{[]string{"bogus", "--slack-user", "U1"}, `unknown kind "bogus"`},
		{[]string{"check-failed"}, "one of --slack-user or --github is required"},
		{[]string{"check-failed", "--slack-user", "U1"}, "SLACK_BOT_TOKEN is not set"},
	}
	for _, tc := range cases {
		err := runSampleDM(tc.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("runSampleDM(%q) = %v, want error containing %q", tc.args, err, tc.want)
		}
	}
}

func TestSampleDMs_RenderEveryKind(t *testing.T) {
	a := sampleArgs{repo: "r", branch: "b", check: "c", url: "u", title: "t", author: "a"}
	for kind, build := range sampleDMs {
		if dm := build(a); len(dm.blocks) == 0 || dm.fallback == "" {
			t.Errorf("%s: empty DM %+v", kind, dm)
		}
	}
}
