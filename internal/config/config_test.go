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
