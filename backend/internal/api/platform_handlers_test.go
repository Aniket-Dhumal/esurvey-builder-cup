package api

import "testing"

// A queue depth is read at a glance or not at all.
func TestCountSeparatesThousands(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{10482, "10,482"},
		{104829, "104,829"},
		{1234567, "1,234,567"},
	} {
		if got := count(tc.in); got != tc.want {
			t.Errorf("count(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
