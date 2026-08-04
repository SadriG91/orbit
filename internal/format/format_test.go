package format

import (
	"testing"
)

func TestHumanTokens(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{0, ""}, {-5, ""}, {940, "940"}, {12_400, "12k"}, {1_500_000, "1.5M"}, {664_500_000, "664M"}} {
		if got := HumanTokens(c.in); got != c.want {
			t.Errorf("HumanTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
