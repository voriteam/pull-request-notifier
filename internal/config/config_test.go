package config

import (
	"reflect"
	"testing"
)

func TestParseJSONStringList(t *testing.T) {
	for v, want := range map[string][]string{
		"":                         nil,
		"[]":                       {},
		"null":                     nil,
		`["production"]`:           {"production"},
		`["production","staging"]`: {"production", "staging"},
	} {
		got, err := parseJSONStringList(v)
		if err != nil || len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("%q: got %v, %v; want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"production", `"production"`, `[1]`, `["a"`} {
		if _, err := parseJSONStringList(v); err == nil {
			t.Errorf("%q: want error", v)
		}
	}
}

func TestParseRegexp(t *testing.T) {
	for _, v := range []string{"", "  "} {
		if re, err := parseRegexp(v); re != nil || err != nil {
			t.Errorf("%q: got %v, %v; want nil", v, re, err)
		}
	}
	re, err := parseRegexp("^policy-bot")
	if err != nil || !re.MatchString("policy-bot: main") || re.MatchString("build") {
		t.Errorf("valid pattern: %v, %v", re, err)
	}
	if _, err := parseRegexp("(policy"); err == nil {
		t.Error("invalid pattern: want error")
	}
}
