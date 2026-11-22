package theme

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The website is dark-only and takes its role colors from this package's
// dark column. These tests read its token file so the two cannot drift, and
// so its text never falls below WCAG AA contrast on its own backgrounds.

const webTokens = "../../../web/assets/tokens.css"

func readWebTokens(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(webTokens)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^\s*--([a-z-]+):\s*(#[0-9A-Fa-f]{6});`).FindAllStringSubmatch(string(raw), -1) {
		tokens[match[1]] = strings.ToUpper(match[2])
	}
	return tokens
}

func TestWebsiteTokensMatchTheDarkPalette(t *testing.T) {
	t.Parallel()
	tokens := readWebTokens(t)
	for name, color := range map[string]lipgloss.CompleteAdaptiveColor{
		"accent": accent, "on-accent": onAccent, "muted": muted, "border": border, "current-row": currentRow,
		"gold": gold, "silver": silver, "bronze": bronze, "up": up, "down": down, "warning": warning, "error": failure,
	} {
		if got, want := tokens[name], strings.ToUpper(color.Dark.TrueColor); got != want {
			t.Errorf("web token --%s = %q, want the dark palette's %q", name, got, want)
		}
	}
}

func TestWebsiteTextMeetsAAContrast(t *testing.T) {
	t.Parallel()
	tokens := readWebTokens(t)
	// WCAG AA for body text. Every pair below is text the site actually sets.
	const minimum = 4.5
	pairs := []struct{ text, background string }{
		{"text", "bg"}, {"text", "panel"}, {"text", "current-row"},
		{"muted", "bg"}, {"muted", "panel"}, {"muted", "current-row"},
		{"accent", "bg"}, {"accent", "panel"}, {"accent", "current-row"},
		{"on-accent", "accent"},
		{"gold", "panel"}, {"silver", "panel"}, {"bronze", "panel"},
		{"up", "bg"}, {"up", "panel"}, {"up", "current-row"},
		{"down", "bg"}, {"down", "panel"}, {"down", "current-row"},
		{"warning", "bg"}, {"warning", "panel"}, {"warning", "current-row"},
		{"error", "bg"}, {"error", "panel"},
	}
	for _, pair := range pairs {
		text, background := tokens[pair.text], tokens[pair.background]
		if text == "" || background == "" {
			t.Errorf("token --%s or --%s is missing", pair.text, pair.background)
			continue
		}
		if ratio := contrast(text, background); ratio < minimum {
			t.Errorf("--%s on --%s is %.2f:1, below %.1f:1", pair.text, pair.background, ratio, minimum)
		}
	}
	// A focus ring is a non-text indicator and needs 3:1.
	for _, background := range []string{"bg", "panel"} {
		if ratio := contrast(tokens["accent"], tokens[background]); ratio < 3 {
			t.Errorf("focus ring on --%s is %.2f:1, below 3:1", background, ratio)
		}
	}
}

// contrast is the WCAG 2 contrast ratio of two #RRGGBB colors.
func contrast(a, b string) float64 {
	lighter, darker := luminance(a), luminance(b)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func luminance(hex string) float64 {
	channel := func(offset int) float64 {
		value, _ := strconv.ParseUint(hex[offset:offset+2], 16, 8)
		c := float64(value) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}
