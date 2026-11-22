package sharecard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"regexp"
	"strings"
	"testing"
)

func render(t *testing.T, card Card) *image.RGBA {
	t.Helper()
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	data, err := renderer.Render(card)
	if err != nil {
		t.Fatal(err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "png" {
		t.Fatalf("card is not a PNG: %q, %v", format, err)
	}
	picture, ok := decoded.(*image.RGBA)
	if !ok {
		t.Fatalf("card decodes as %T, want *image.RGBA", decoded)
	}
	return picture
}

// count returns how many pixels of one color lie inside a rectangle.
func count(picture *image.RGBA, area image.Rectangle, ink color.RGBA) int {
	total := 0
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if picture.RGBAAt(x, y) == ink {
				total++
			}
		}
	}
	return total
}

func TestEveryCardIsALargeImageCard(t *testing.T) {
	t.Parallel()
	for name, card := range sampleCards() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			picture := render(t, card)
			if size := picture.Bounds().Size(); size != (image.Point{X: Width, Y: Height}) || Width != 1200 || Height != 630 {
				t.Fatalf("card is %v, want 1200x630", size)
			}
			// The page shows at every corner and the panel in the middle.
			for _, corner := range []image.Point{{0, 0}, {Width - 1, 0}, {0, Height - 1}, {Width - 1, Height - 1}} {
				if got := picture.RGBAAt(corner.X, corner.Y); got != colorPage {
					t.Errorf("corner %v = %v, want the page color", corner, got)
				}
			}
			if got := picture.RGBAAt(panelLeft+12, panelBottom-12); got != colorPanel {
				t.Errorf("panel interior = %v, want the panel color", got)
			}
			// The wordmark's caret is the accent, and nothing else in the
			// header is.
			header := image.Rect(0, 0, Width, panelTop-24)
			if caret := count(picture, image.Rect(margin, 40, margin+70, 100), colorAccent); caret == 0 || caret != count(picture, header, colorAccent) {
				t.Errorf("accent pixels in the header = %d, in the caret = %d", count(picture, header, colorAccent), caret)
			}
			// Title, section, content, and footer each leave ink.
			for region, area := range map[string]image.Rectangle{
				"wordmark": image.Rect(margin+70, 40, 520, 100),
				"title":    image.Rect(contentLeft, panelTop-20, contentEnd, panelTop+20),
				"content":  image.Rect(contentLeft, panelTop+110, contentEnd, panelBottom-70),
				"note":     image.Rect(contentLeft, panelBottom-64, contentEnd, panelBottom-20),
				"command":  image.Rect(margin+30, Height-84, 440, Height-36),
			} {
				if count(picture, area, colorText)+count(picture, area, colorMuted) == 0 {
					t.Errorf("%s region is empty", region)
				}
			}
			// Nothing spills into the margins beside the panel, however long
			// the text.
			for side, area := range map[string]image.Rectangle{
				"left":  image.Rect(0, panelTop-24, panelLeft, panelBottom),
				"right": image.Rect(panelRight, panelTop-24, Width, panelBottom),
			} {
				if clean := count(picture, area, colorPage); clean != area.Dx()*area.Dy() {
					t.Errorf("%s margin has %d drawn pixels", side, area.Dx()*area.Dy()-clean)
				}
			}
		})
	}
}

func TestRenderIsDeterministicAndFollowsContent(t *testing.T) {
	t.Parallel()
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	card := sampleCards()["profile"]
	first, err := renderer.Render(card)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderer.Render(card)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("the same card rendered differently (%v)", err)
	}
	changed := card
	changed.Tiles = append([]Tile(nil), card.Tiles...)
	changed.Tiles[1].Value = "≥$1,180.43"
	third, err := renderer.Render(changed)
	if err != nil || bytes.Equal(first, third) {
		t.Fatalf("a different estimate rendered the same image (%v)", err)
	}
	if card.Hash() == changed.Hash() || card.Hash() != sampleCards()["profile"].Hash() || len(card.Hash()) != 16 {
		t.Fatalf("hash %q does not track content (changed %q)", card.Hash(), changed.Hash())
	}
	recolored := sampleCards()["board"]
	recolored.Rows = append([]Row(nil), recolored.Rows...)
	recolored.Rows[0].Tone = TonePlain
	if recolored.Hash() == sampleCards()["board"].Hash() {
		t.Fatal("hash ignores a row's tone, which changes the image")
	}
}

func TestRankTonesReachTheImage(t *testing.T) {
	t.Parallel()
	picture := render(t, sampleCards()["board"])
	ranks := image.Rect(contentLeft, panelTop+110, contentLeft+70, panelBottom-70)
	for name, ink := range map[string]color.RGBA{"gold": colorGold, "silver": colorSilver, "bronze": colorBronze} {
		if count(picture, ranks, ink) == 0 {
			t.Errorf("no %s rank was drawn", name)
		}
	}
	unranked := render(t, sampleCards()["board-unranked"])
	if count(unranked, ranks, colorGold)+count(unranked, ranks, colorSilver)+count(unranked, ranks, colorBronze) != 0 {
		t.Error("an unranked board drew a medal color")
	}
}

func TestTextAndAltCarryTheCardsContent(t *testing.T) {
	t.Parallel()
	card := sampleCards()["profile"]
	text := strings.Join(card.Text(), "\n")
	for _, want := range []string{">_ TOKENHOLIC", "@dana", "Last 30 days", "$ Est", "≥$1,180.42", "≥4.26B", "92.3%", "$ npx @tokenholic/cli", "never spending"} {
		if !strings.Contains(text, want) {
			t.Errorf("card text lacks %q:\n%s", want, text)
		}
	}
	if alt := card.Alt(); alt != "tokenholic public profile: @dana, Last 30 days, Tokens ≥4.26B, $ Est ≥$1,180.42, Priced 92.3%, All-time  ≥21.9B tokens · ≥$6,204.18 est." {
		t.Errorf("alt = %q", alt)
	}
	if alt := sampleCards()["board-unranked"].Alt(); !strings.Contains(alt, "— @dana $1,180.00") {
		t.Errorf("board alt = %q", alt)
	}
}

// The card's colors are the website's, which a test in internal/tui/theme
// ties to the terminal palette.
func TestPaletteMatchesTheWebsiteTokens(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../web/assets/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^\s*--([a-z-]+):\s*(#[0-9A-Fa-f]{6});`).FindAllStringSubmatch(string(raw), -1) {
		tokens[match[1]] = strings.ToUpper(match[2])
	}
	for name, ink := range map[string]color.RGBA{
		"bg": colorPage, "panel": colorPanel, "text": colorText, "accent": colorAccent, "muted": colorMuted,
		"border": colorBorder, "gold": colorGold, "silver": colorSilver, "bronze": colorBronze,
	} {
		if got := "#" + strings.ToUpper(hex.EncodeToString([]byte{ink.R, ink.G, ink.B})); got != tokens[name] {
			t.Errorf("card color %s = %s, website token is %q", name, got, tokens[name])
		}
	}
}

// The embedded fonts are the recorded upstream files, and the pixel face is
// byte-for-byte the website's copy.
func TestFontsAreTheRecordedFiles(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("fonts/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Silkscreen-Regular.ttf", "Silkscreen-Bold.ttf", "JetBrainsMono-Regular.ttf", "JetBrainsMono-Bold.ttf"} {
		data, err := fontFiles.ReadFile("fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if line := hex.EncodeToString(sum[:]) + "  " + name; !strings.Contains(string(readme), line) {
			t.Errorf("fonts/README.md does not record %q", line)
		}
		if strings.HasPrefix(name, "Silkscreen") {
			web, err := os.ReadFile("../../web/assets/fonts/" + name)
			if err != nil || !bytes.Equal(web, data) {
				t.Errorf("%s differs from the website's copy (%v)", name, err)
			}
		}
	}
	for _, license := range []string{"fonts/Silkscreen-OFL.txt", "fonts/JetBrainsMono-OFL.txt"} {
		if text, err := os.ReadFile(license); err != nil || !strings.Contains(string(text), "SIL Open Font License, Version 1.1") {
			t.Errorf("%s is missing or is not the OFL (%v)", license, err)
		}
	}
}

func TestPNGDecodesWithTheStandardLibraryOnly(t *testing.T) {
	t.Parallel()
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	data, err := renderer.Render(Card{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != Width || config.Height != Height {
		t.Fatalf("empty card = %dx%d, %v", config.Width, config.Height, err)
	}
}
