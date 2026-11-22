// Package sharecard renders the 1200×630 PNG cards that unfurl when a
// tokenholic link is shared. Crawlers do not run JavaScript, so the API
// draws these itself.
//
// The package only draws. A Card holds display text that its caller has
// already formatted: how a value becomes text, including the lower-bound and
// unavailable forms, is decided where the data is read, so nothing here can
// turn a missing number into a zero. Cards use the dark column of the palette
// in docs/design.md and the website's two typefaces.
package sharecard

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Width and Height are the card's size in pixels: the large-image size
// social sites expect, at a 1.91:1 ratio.
const (
	Width  = 1200
	Height = 630
)

// layoutVersion is part of every card's hash. Bump it when the drawing
// changes, so cached images are replaced.
const layoutVersion = "1"

// Tone picks a rank's color. Rank carries the meaning in text; the tone only
// decorates it.
type Tone int

const (
	// TonePlain is the ordinary text color.
	TonePlain Tone = iota
	// ToneGold, ToneSilver, and ToneBronze mark ranks one to three.
	ToneGold
	ToneSilver
	ToneBronze
	// ToneMuted is for a row without a rank.
	ToneMuted
)

// Tile is one labelled headline value, such as "$ EST" over "≥$1,180.42".
type Tile struct {
	Label string
	Value string
}

// Row is one line of standings.
type Row struct {
	Rank  string
	Tone  Tone
	Name  string
	Value string
}

// Card is everything one card draws, as display text.
type Card struct {
	// Kind names what the card is, top right: "PUBLIC PROFILE".
	Kind string
	// Title sits in the panel's top border: a login or a group name.
	Title string
	// Section labels the panel's content: the period, and the metric for a
	// board.
	Section string
	// Tiles are headline values. A card has tiles or rows, not both.
	Tiles []Tile
	// Rows are standings.
	Rows []Row
	// Note is one line under the tiles or rows.
	Note string
	// Footnote is the small print beside the install command. It is where
	// an estimate is called an estimate.
	Footnote string
}

// Text returns every string the card draws, in drawing order. It is the
// card's content without its pixels, for tests and for image alt text.
func (c Card) Text() []string {
	text := []string{wordmarkCaret + " " + wordmarkName, c.Kind, c.Title, c.Section}
	for _, tile := range c.Tiles {
		text = append(text, tile.Label, tile.Value)
	}
	for _, row := range c.Rows {
		text = append(text, row.Rank, row.Name, row.Value)
	}
	return append(text, c.Note, installPrompt+" "+installCommand, c.Footnote)
}

// Hash identifies the card's content and layout. Two cards with the same
// hash draw the same image, so it serves as a cache key and entity tag
// without rendering anything.
func (c Card) Hash() string {
	digest := sha256.New()
	digest.Write([]byte(layoutVersion))
	for _, text := range c.Text() {
		digest.Write([]byte{0})
		digest.Write([]byte(text))
	}
	for _, row := range c.Rows {
		digest.Write([]byte{0, byte(row.Tone)})
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// Alt describes the card in one sentence for people who cannot see it.
func (c Card) Alt() string {
	parts := []string{c.Title, c.Section}
	for _, tile := range c.Tiles {
		parts = append(parts, tile.Label+" "+tile.Value)
	}
	for _, row := range c.Rows {
		parts = append(parts, strings.TrimSpace(row.Rank+" "+row.Name+" "+row.Value))
	}
	parts = append(parts, c.Note)
	kept := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	return "tokenholic " + strings.ToLower(c.Kind) + ": " + strings.Join(kept, ", ") + "."
}
