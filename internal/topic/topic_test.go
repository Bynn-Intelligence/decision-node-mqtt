package topic

import (
	"reflect"
	"testing"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		filter, name string
		want         []string
		ok           bool
	}{
		{"decisionnode/request/+", "decisionnode/request/cam1", []string{"cam1"}, true},
		{"decisionnode/request/+", "decisionnode/request/cam1/extra", nil, false},
		{"decisionnode/request/+", "decisionnode/request", nil, false},
		{"decisionnode/request/#", "decisionnode/request/site/cam1", []string{"site", "cam1"}, true},
		{"decisionnode/request/#", "decisionnode/request", []string{}, true},
		{"site/+/request/+", "site/a/request/b", []string{"a", "b"}, true},
		{"exact/topic", "exact/topic", nil, true},
		{"exact/topic", "exact/other", nil, false},
		{"#", "$SYS/broker", nil, false},
		{"+/x", "$SYS/x", nil, false},
	} {
		got, ok := Captures(c.filter, c.name)
		if ok != c.ok || (ok && len(c.want) > 0 && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s ~ %s: %v %v", c.filter, c.name, got, ok)
		}
	}
}

func TestValid(t *testing.T) {
	for f, want := range map[string]bool{"a/+/b": true, "a/#": true, "#": true, "a/#/b": false, "a+/b": false, "": false, "a/b#": false} {
		if ValidFilter(f) != want {
			t.Errorf("filter %q", f)
		}
	}
	for n, want := range map[string]bool{"a/b": true, "a/+": false, "a/#": false, "": false} {
		if ValidName(n) != want {
			t.Errorf("name %q", n)
		}
	}
}
