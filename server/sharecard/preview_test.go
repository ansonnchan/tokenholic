package sharecard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWritePreviews writes sample cards for review when SHARECARD_PREVIEW
// names a directory. It is skipped otherwise.
func TestWritePreviews(t *testing.T) {
	directory := os.Getenv("SHARECARD_PREVIEW")
	if directory == "" {
		t.Skip("SHARECARD_PREVIEW is not set")
	}
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for name, card := range sampleCards() {
		data, err := renderer.Render(card)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name+".png"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// sampleCards are synthetic cards covering each layout and its edge cases.
func sampleCards() map[string]Card {
	const footnote = "Est = API list prices, never spending · self-reported"
	return map[string]Card{
		"profile": {
			Kind: "Public profile", Title: "@dana", Section: "Last 30 days",
			Tiles:    []Tile{{"Tokens", "≥4.26B"}, {"$ Est", "≥$1,180.42"}, {"Priced", "92.3%"}},
			Note:     "All-time  ≥21.9B tokens · ≥$6,204.18 est",
			Footnote: footnote,
		},
		"profile-unknown": {
			Kind: "Public profile", Title: "@newbie", Section: "Last 30 days",
			Tiles:    []Tile{{"Tokens", "—"}, {"$ Est", "—"}, {"Priced", "—"}},
			Note:     "No usage in the last 30 days · all-time  987 tokens · no estimate",
			Footnote: footnote,
		},
		"profile-long": {
			Kind: "Public profile", Title: "@juno-with-a-very-long-github-login-indeed", Section: "Last 30 days",
			Tiles:    []Tile{{"Tokens", "≥18,446,744T"}, {"$ Est", "≥$1,234,567,890.12"}, {"Priced", "94.9%"}},
			Note:     "All-time  ≥18,446,744T tokens · ≥$1,234,567,890.12 est",
			Footnote: footnote,
		},
		"board": {
			Kind: "Public board", Title: "night-shift", Section: "This week · $ est",
			Rows: []Row{
				{"1", ToneGold, "@dana", "$1,180.00"}, {"2", ToneSilver, "@kai", "$1,090.00"}, {"3", ToneBronze, "@rio", "$840.50"},
			},
			Note:     "9 public members",
			Footnote: footnote,
		},
		"board-tokens": {
			Kind: "Public board", Title: "night-shift", Section: "All-time · tokens",
			Rows: []Row{
				{"1", ToneGold, "@kai", "4.27B"}, {"2", ToneSilver, "@dana", "≥4.26B"}, {"3", ToneBronze, "@juno-with-a-very-long-github-login", "2.1B"},
			},
			Note:     "9 public members",
			Footnote: "Usage is self-reported",
		},
		"board-unranked": {
			Kind: "Public board", Title: "two of us ☕", Section: "This week · $ est",
			Rows: []Row{
				{"—", ToneMuted, "@dana", "$1,180.00"}, {"—", ToneMuted, "@kai", "≥$560.75"},
			},
			Note:     "Ranks appear once 3 members can be ranked",
			Footnote: footnote,
		},
	}
}
