package format

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestSliceRunesNeverSplitsACharacter(t *testing.T) {
	s := "aé日🙂bcé日🙂d" // 1,2,3,4-byte characters, mixed
	for start := 0; start <= len(s); start++ {
		for end := start; end <= len(s); end++ {
			got := SliceRunes(s, start, end)
			if !utf8.ValidString(got) {
				t.Fatalf("SliceRunes(%d,%d) = % x, not valid UTF-8", start, end, got)
			}
			if got != "" && !strings.Contains(s, got) {
				t.Fatalf("SliceRunes(%d,%d) = %q, not a substring", start, end, got)
			}
		}
	}
	if got := SliceRunes(s, -5, 1000); got != s {
		t.Errorf("out-of-range bounds should clamp to the whole string, got %q", got)
	}
	if got := SliceRunes(s, 4, 2); got != "" {
		t.Errorf("an inverted range should be empty, got %q", got)
	}
}
