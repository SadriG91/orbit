package format

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func Truncate(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

func Pad(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func RelTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return Itoa(int(d.Hours())) + "h"
	case d < 365*24*time.Hour:
		return Itoa(int(d.Hours()/24)) + "d"
	}
	return Itoa(int(d.Hours()/24/365)) + "y"
}

func Itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// HumanTokens keeps the column narrow: these run to hundreds of millions once
// cache reads are counted, and the exact figure never matters at a glance.
func HumanTokens(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return Itoa(int(n))
	case n < 1_000_000:
		return Itoa(int(n/1000)) + "k"
	case n < 100_000_000:
		v := float64(n) / 1_000_000
		whole := int(v)
		frac := int((v - float64(whole)) * 10)
		if whole < 10 && frac > 0 {
			return Itoa(whole) + "." + Itoa(frac) + "M"
		}
		return Itoa(whole) + "M"
	}
	return Itoa(int(n/1_000_000)) + "M"
}

// Clean strips control characters that would corrupt the layout when echoing
// captured pane output or a pasted prompt back into the UI.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Home joins a path under the user's home directory. Every package needs it and
// none of them should be reaching for os.UserHomeDir individually.
func Home(rest ...string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(append([]string{h}, rest...)...)
}
