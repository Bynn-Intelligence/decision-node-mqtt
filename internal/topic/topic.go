// Package topic matches MQTT topic names against topic filters.
package topic

import "strings"

// ValidName reports whether s is a topic name a message can be published
// to: not empty, no wildcard, no NUL.
func ValidName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "+#\x00")
}

// ValidFilter reports whether s is a topic filter: "+" only as a whole
// level, "#" only as the whole last level.
func ValidFilter(s string) bool {
	if s == "" || strings.Contains(s, "\x00") {
		return false
	}
	levels := strings.Split(s, "/")
	for i, l := range levels {
		switch {
		case l == "#":
			if i != len(levels)-1 {
				return false
			}
		case l == "+":
		case strings.ContainsAny(l, "+#"):
			return false
		}
	}
	return true
}

// Match reports whether the topic name matches the filter. A name whose
// first level starts with "$" matches no filter that starts with a wildcard.
func Match(filter, name string) bool {
	_, ok := Captures(filter, name)
	return ok
}

// Captures returns the levels of the name that the filter's wildcards
// matched, in order, and whether the name matches at all.
func Captures(filter, name string) ([]string, bool) {
	f := strings.Split(filter, "/")
	n := strings.Split(name, "/")
	if strings.HasPrefix(name, "$") && (f[0] == "+" || f[0] == "#") {
		return nil, false
	}
	var out []string
	for i, l := range f {
		if l == "#" {
			out = append(out, n[i:]...)
			return out, true
		}
		if i >= len(n) {
			return nil, false
		}
		switch l {
		case "+":
			out = append(out, n[i])
		default:
			if l != n[i] {
				return nil, false
			}
		}
	}
	if len(n) != len(f) {
		return nil, false
	}
	return out, true
}
