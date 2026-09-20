package web

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
)

// themeBlocks are the three authoritative token blocks in styles.css. The demo
// toggles between them and the OS-preference logic (app.js) selects among them,
// so every declared colour pair must clear its threshold in all three.
var themeBlocks = map[string]string{
	"light":    ":root {",
	"dark":     `:root[data-theme="dark"] {`,
	"contrast": `:root[data-theme="contrast"] {`,
}

// textPairs need 4.5:1 (WCAG AA body text); iconPairs need 3:1 (large text,
// icons and UI component boundaries, WCAG 1.4.3 / 1.4.11).
var (
	textPairs = [][2]string{
		{"--fg", "--bg"}, {"--fg", "--surface"},
		{"--muted", "--bg"}, {"--muted", "--surface"},
		{"--link", "--bg"}, {"--link", "--surface"},
	}
	iconPairs = [][2]string{
		{"--accent-pass", "--bg"}, {"--accent-pass", "--surface"},
		{"--accent-fail", "--bg"}, {"--accent-fail", "--surface"},
		{"--focus", "--bg"}, {"--focus", "--surface"},
		{"--border", "--bg"}, {"--border", "--surface"},
	}
)

var tokenRE = regexp.MustCompile(`(--[a-z-]+):\s*(#[0-9a-fA-F]{6})\s*;`)

func TestContrastAllThemes(t *testing.T) {
	css := readAsset(t, "styles.css")
	for theme, selector := range themeBlocks {
		tokens := parseTokens(t, css, selector)
		for _, name := range []string{"--fg", "--bg", "--surface", "--muted", "--link", "--accent-pass", "--accent-fail", "--border", "--focus"} {
			if _, ok := tokens[name]; !ok {
				t.Fatalf("theme %s: missing token %s", theme, name)
			}
		}
		check := func(pairs [][2]string, min float64, kind string) {
			for _, p := range pairs {
				r := ratio(tokens[p[0]], tokens[p[1]])
				if r < min {
					t.Errorf("theme %s: %s on %s = %.2f:1, want >= %.1f:1 (%s)", theme, p[0], p[1], r, min, kind)
				}
			}
		}
		check(textPairs, 4.5, "text")
		check(iconPairs, 3.0, "icon/large")
	}
}

func parseTokens(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	i := strings.Index(css, selector)
	if i < 0 {
		t.Fatalf("selector %q not found in styles.css", selector)
	}
	body := css[i+len(selector):]
	if end := strings.Index(body, "}"); end >= 0 {
		body = body[:end]
	}
	out := map[string]string{}
	for _, m := range tokenRE.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// ratio is the WCAG contrast ratio between two #rrggbb colours.
func ratio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	r := channel(hex[1:3])
	g := channel(hex[3:5])
	b := channel(hex[5:7])
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func channel(h string) float64 {
	var v int
	_, _ = fmt.Sscanf(h, "%x", &v)
	c := float64(v) / 255.0
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}
