package sharecard

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The fonts are the website's two families, unmodified and OFL-licensed.
// fonts/README.md records their sources and checksums.
//
//go:embed fonts/Silkscreen-Regular.ttf fonts/Silkscreen-Bold.ttf fonts/JetBrainsMono-Regular.ttf fonts/JetBrainsMono-Bold.ttf
var fontFiles embed.FS

// The dark column of the palette in docs/design.md, as the website uses it.
// A test compares these with web/assets/tokens.css.
var (
	colorPage   = color.RGBA{0x0B, 0x11, 0x20, 0xFF}
	colorPanel  = color.RGBA{0x11, 0x1A, 0x2E, 0xFF}
	colorText   = color.RGBA{0xE8, 0xEA, 0xF0, 0xFF}
	colorAccent = color.RGBA{0xD9, 0x80, 0x5F, 0xFF}
	colorMuted  = color.RGBA{0x8D, 0x92, 0x9B, 0xFF}
	colorBorder = color.RGBA{0x50, 0x55, 0x5E, 0xFF}
	colorGold   = color.RGBA{0xD9, 0xB4, 0x5B, 0xFF}
	colorSilver = color.RGBA{0xBC, 0xC2, 0xCB, 0xFF}
	colorBronze = color.RGBA{0xC0, 0x82, 0x53, 0xFF}
)

const (
	wordmarkCaret  = ">_"
	wordmarkName   = "TOKENHOLIC"
	installPrompt  = "$"
	installCommand = "npx @tokenholic/cli"
)

// Layout, in pixels. The pixel face is drawn on an 8-pixel grid, so its
// sizes are multiples of 8 and its glyphs land on whole pixels.
const (
	margin      = 56
	panelLeft   = margin
	panelRight  = Width - margin
	panelTop    = 140
	panelBottom = 510
	panelInset  = 44
	contentLeft = panelLeft + panelInset
	contentEnd  = panelRight - panelInset
	radius      = 10
	borderWidth = 2
	// tracking spaces out uppercase labels, as the website's labels are.
	tracking = 0.14
)

// Renderer draws cards. It holds the parsed fonts, which are read-only, so
// one Renderer serves concurrent requests.
type Renderer struct {
	display, displayBold, mono, monoBold *sfnt.Font
}

// New parses the embedded fonts.
func New() (*Renderer, error) {
	renderer := &Renderer{}
	for name, target := range map[string]**sfnt.Font{
		"Silkscreen-Regular.ttf": &renderer.display, "Silkscreen-Bold.ttf": &renderer.displayBold,
		"JetBrainsMono-Regular.ttf": &renderer.mono, "JetBrainsMono-Bold.ttf": &renderer.monoBold,
	} {
		data, err := fontFiles.ReadFile("fonts/" + name)
		if err != nil {
			return nil, fmt.Errorf("read share-card font %s: %w", name, err)
		}
		parsed, err := opentype.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse share-card font %s: %w", name, err)
		}
		*target = parsed
	}
	return renderer, nil
}

// Render draws the card and returns it as a PNG. The same card always
// produces the same bytes on one platform.
func (r *Renderer) Render(card Card) ([]byte, error) {
	canvas := &canvas{renderer: r, image: image.NewRGBA(image.Rect(0, 0, Width, Height))}
	draw.Draw(canvas.image, canvas.image.Bounds(), image.NewUniform(colorPage), image.Point{}, draw.Src)
	if err := canvas.draw(card); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas.image); err != nil {
		return nil, fmt.Errorf("encode share card: %w", err)
	}
	return encoded.Bytes(), nil
}

// canvas is one card being drawn. Faces are created per card because a face
// caches glyphs and is not safe to share between goroutines.
type canvas struct {
	renderer *Renderer
	image    *image.RGBA
	err      error
}

func (c *canvas) draw(card Card) error {
	// Header: wordmark left, kind right.
	wordmark := c.face(c.renderer.displayBold, 40)
	caretEnd := c.text(wordmark, margin, 92, wordmarkCaret, colorAccent, 0.04)
	c.text(wordmark, caretEnd+22, 92, wordmarkName, colorText, 0.04)
	label := c.face(c.renderer.monoBold, 20)
	c.textRight(label, panelRight, 86, strings.ToUpper(card.Kind), colorMuted, tracking)

	// Panel: a rounded border with the title set into its top edge.
	c.roundedRect(panelLeft, panelTop, panelRight, panelBottom, radius, colorBorder)
	c.roundedRect(panelLeft+borderWidth, panelTop+borderWidth, panelRight-borderWidth, panelBottom-borderWidth, radius-borderWidth, colorPanel)
	title, titleFace := c.displayText(card.Title, 32)
	title = c.fit(titleFace, title, contentEnd-contentLeft-48, 0.02)
	titleWidth := c.measure(titleFace, title, 0.02)
	c.fill(contentLeft-14, panelTop-20, contentLeft+titleWidth+14, panelTop, colorPage)
	c.fill(contentLeft-14, panelTop, contentLeft+titleWidth+14, panelTop+20, colorPanel)
	c.text(titleFace, contentLeft, panelTop+12, title, colorText, 0.02)

	// Section label and dashed rule.
	section := strings.ToUpper(card.Section)
	sectionEnd := c.text(label, contentLeft, panelTop+78, section, colorMuted, tracking)
	c.dashes(sectionEnd+18, contentEnd, panelTop+71, colorBorder)

	switch {
	case len(card.Tiles) > 0:
		c.tiles(card.Tiles, label)
	case len(card.Rows) > 0:
		c.rows(card.Rows)
	}
	if card.Note != "" {
		note := c.face(c.renderer.mono, 24)
		c.text(note, contentLeft, panelBottom-32, c.fit(note, card.Note, contentEnd-contentLeft, 0), colorMuted, 0)
	}

	// Footer: the install command, and the small print.
	command := c.face(c.renderer.monoBold, 28)
	promptEnd := c.text(command, margin, Height-52, installPrompt, colorAccent, 0)
	commandEnd := c.text(command, promptEnd+16, Height-52, installCommand, colorText, 0)
	small := c.face(c.renderer.monoBold, 16)
	footnote := c.fit(small, strings.ToUpper(card.Footnote), panelRight-commandEnd-40, tracking)
	c.textRight(small, panelRight, Height-55, footnote, colorMuted, tracking)
	return c.err
}

// tiles draws up to three headline values in a row, at the largest size at
// which they all fit, so a long estimate shrinks instead of overflowing.
func (c *canvas) tiles(tiles []Tile, label font.Face) {
	const gap, minTileWidth = 56, 200
	var value font.Face
	widths := make([]int, len(tiles))
	for _, size := range []float64{84, 76, 68, 60, 52, 44, 36} {
		value = c.face(c.renderer.monoBold, size)
		total := gap * (len(tiles) - 1)
		for index, tile := range tiles {
			// A minimum keeps short values, such as the unavailable glyph,
			// from bunching at the left.
			widths[index] = max(c.measure(value, tile.Value, 0), c.measure(label, strings.ToUpper(tile.Label), tracking), minTileWidth)
			total += widths[index]
		}
		if total <= contentEnd-contentLeft {
			break
		}
	}
	x := contentLeft
	for index, tile := range tiles {
		c.text(label, x, panelTop+150, strings.ToUpper(tile.Label), colorMuted, tracking)
		c.text(value, x, panelTop+250, c.fit(value, tile.Value, contentEnd-x, 0), colorText, 0)
		x += widths[index] + gap
	}
}

// rows draws standings: rank, name, and value, with the value right-aligned
// and a long name cut short before it reaches the value.
func (c *canvas) rows(rows []Row) {
	const pitch = 62
	face := c.face(c.renderer.monoBold, 44)
	rankWidth := c.measure(face, "00", 0)
	for index, row := range rows {
		baseline := panelTop + 152 + index*pitch
		c.textRight(face, contentLeft+rankWidth, baseline, row.Rank, toneColor(row.Tone), 0)
		valueStart := c.textRight(face, contentEnd, baseline, row.Value, colorText, 0)
		nameStart := contentLeft + rankWidth + 36
		c.text(face, nameStart, baseline, c.fit(face, row.Name, valueStart-nameStart-36, 0), colorText, 0)
	}
}

func toneColor(tone Tone) color.RGBA {
	switch tone {
	case ToneGold:
		return colorGold
	case ToneSilver:
		return colorSilver
	case ToneBronze:
		return colorBronze
	case ToneMuted:
		return colorMuted
	default:
		return colorText
	}
}

func (c *canvas) face(source *sfnt.Font, size float64) font.Face {
	face, err := opentype.NewFace(source, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil && c.err == nil {
		c.err = fmt.Errorf("size share-card font: %w", err)
	}
	return face
}

// displayText returns text in the pixel face when it has every glyph, and in
// the monospace otherwise: the pixel face has capitals and digits only.
func (c *canvas) displayText(text string, size float64) (string, font.Face) {
	upper := strings.ToUpper(text)
	var buffer sfnt.Buffer
	for _, character := range upper {
		if index, err := c.renderer.display.GlyphIndex(&buffer, character); err != nil || index == 0 {
			return text, c.face(c.renderer.monoBold, size)
		}
	}
	return upper, c.face(c.renderer.display, size)
}

// measure is the width of text in whole pixels, including tracking.
func (c *canvas) measure(face font.Face, text string, track float64) int {
	if face == nil {
		return 0
	}
	width := font.MeasureString(face, text)
	return width.Ceil() + c.trackPixels(face, track)*max(len([]rune(text))-1, 0)
}

func (c *canvas) trackPixels(face font.Face, track float64) int {
	if face == nil || track == 0 {
		return 0
	}
	return int(float64(face.Metrics().Height.Ceil())*track*0.76 + 0.5)
}

// fit shortens text with an ellipsis until it is no wider than limit.
func (c *canvas) fit(face font.Face, text string, limit int, track float64) string {
	if c.measure(face, text, track) <= limit {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		if candidate := strings.TrimRight(string(runes), " ") + "…"; c.measure(face, candidate, track) <= limit {
			return candidate
		}
	}
	return ""
}

// text draws left-aligned text with its baseline at y and returns the x
// where it ends. A glyph the font lacks is drawn as a question mark, never
// as an empty box.
func (c *canvas) text(face font.Face, x, y int, text string, ink color.RGBA, track float64) int {
	if face == nil {
		return x
	}
	drawer := font.Drawer{Dst: c.image, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(x, y)}
	step := fixed.I(c.trackPixels(face, track))
	for _, character := range text {
		if _, ok := face.GlyphAdvance(character); !ok {
			character = '?'
		}
		drawer.DrawString(string(character))
		drawer.Dot.X += step
	}
	return drawer.Dot.X.Ceil() - step.Ceil()
}

// textRight draws text ending at x and returns the x where it starts.
func (c *canvas) textRight(face font.Face, x, y int, text string, ink color.RGBA, track float64) int {
	start := x - c.measure(face, text, track)
	c.text(face, start, y, text, ink, track)
	return start
}

func (c *canvas) fill(left, top, right, bottom int, ink color.RGBA) {
	draw.Draw(c.image, image.Rect(left, top, right, bottom), image.NewUniform(ink), image.Point{}, draw.Src)
}

// dashes draws a dashed horizontal rule.
func (c *canvas) dashes(left, right, y int, ink color.RGBA) {
	for x := left; x+8 <= right; x += 14 {
		c.fill(x, y, x+8, y+2, ink)
	}
}

// roundedRect fills a rectangle with rounded corners.
func (c *canvas) roundedRect(left, top, right, bottom, r int, ink color.RGBA) {
	x0, y0, x1, y1, corner := float32(left), float32(top), float32(right), float32(bottom), float32(r)
	// A quarter circle as one cubic curve.
	const k = 0.5523
	path := vector.NewRasterizer(Width, Height)
	path.MoveTo(x0+corner, y0)
	path.LineTo(x1-corner, y0)
	path.CubeTo(x1-corner+corner*k, y0, x1, y0+corner-corner*k, x1, y0+corner)
	path.LineTo(x1, y1-corner)
	path.CubeTo(x1, y1-corner+corner*k, x1-corner+corner*k, y1, x1-corner, y1)
	path.LineTo(x0+corner, y1)
	path.CubeTo(x0+corner-corner*k, y1, x0, y1-corner+corner*k, x0, y1-corner)
	path.LineTo(x0, y0+corner)
	path.CubeTo(x0, y0+corner-corner*k, x0+corner-corner*k, y0, x0+corner, y0)
	path.ClosePath()
	path.Draw(c.image, c.image.Bounds(), image.NewUniform(ink), image.Point{})
}
