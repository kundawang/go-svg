package svg_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/twpayne/go-svg"
	"github.com/twpayne/go-svg/svgpath"
)

// badgeDocument returns a badge whose gradient and clip path use the same IDs
// as every other badge. Names are also repeated across the two badges, so the
// fixture exercises ID renaming, reference rewriting, and defs pruning at
// once. The unused gradient must be dropped by composition.
func badgeDocument(t *testing.T, name, from, to string) *svg.Document {
	t.Helper()
	return svg.NewDocument(name, svg.New().
		ViewBox(0, 0, 120, 40).
		AppendChildren(
			svg.Defs(
				svg.LinearGradient().ID("badge-gradient").
					X1("0").Y1("0").X2("0").Y2("1").
					AppendChildren(
						svg.Stop().Offset("0").StopColor(svg.String(from)),
						svg.Stop().Offset("1").StopColor(svg.String(to)),
					),
				svg.ClipPath().ID("badge-clip").AppendChildren(
					svg.Rect().XYWidthHeight(0, 0, 120, 40, svg.Number).RXRY(6, 6, svg.Number),
				),
				svg.LinearGradient().ID("badge-unused").AppendChildren(
					svg.Stop().Offset("0").StopColor("gray"),
				),
			),
			svg.Rect().XYWidthHeight(0, 0, 120, 40, svg.Number).
				Fill("url(#badge-gradient)").
				ClipPath("url(#badge-clip)"),
			svg.Text().XY(60, 26, svg.Number).TextAnchor("middle").Fill("white").
				AppendChildren(svg.CharData(name)),
		))
}

func TestComposeBadges(t *testing.T) {
	first := badgeDocument(t, "alpha", "#123456", "#654321")
	second := badgeDocument(t, "beta", "#00ff00", "#0000ff")

	// Writing a single document must not change because composition exists.
	var before bytes.Buffer
	_, err := first.Root.WriteToIndent(&before, "", "  ")
	assert.NoError(t, err)

	canvas, err := svg.Compose(
		svg.Placement{Document: first},
		svg.Placement{Document: second}.At(130, 0),
	)
	assert.NoError(t, err)

	var after bytes.Buffer
	_, err = first.Root.WriteToIndent(&after, "", "  ")
	assert.NoError(t, err)
	assert.Equal(t, before.String(), after.String(), "composition must not mutate the source document")

	var buffer bytes.Buffer
	if _, err := canvas.WriteToIndent(&buffer, "", "  "); err != nil {
		t.Fatal(err)
	}
	actual := buffer.Bytes()
	filename := filepath.Join("testdata", "compose-badges.svg")
	if os.Getenv("GO_SVG_WRITE_TESTDATA") == "1" {
		assert.NoError(t, os.WriteFile(filename, actual, 0o666)) //nolint:gosec
	} else {
		expected, err := os.ReadFile(filename)
		assert.NoError(t, err)
		assertEquivalentXML(t, expected, actual)
	}

	// The composed document must define every ID it uses, and no ID twice.
	ids, references := documentIDsAndReferences(t, actual)
	assert.Equal(t, len(ids), len(uniqueStrings(ids)), "composed document must not define an ID twice")
	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	for reference := range references {
		_, ok := idSet[reference]
		assert.True(t, ok, "reference #%s has no definition", reference)
	}

	// The two badges must not share IDs, or one gradient would color both.
	assert.Contains(t, string(actual), `id="badge-gradient"`)
	assert.Contains(t, string(actual), `id="beta-2-badge-gradient"`)
	assert.Contains(t, string(actual), `fill="url(#badge-gradient)"`)
	assert.Contains(t, string(actual), `fill="url(#beta-2-badge-gradient)"`)
	assert.NotContains(t, string(actual), "badge-unused", "unreferenced defs must be pruned")
	assert.Contains(t, string(actual), "#123456")
	assert.Contains(t, string(actual), "#00ff00")

	// The second placement must carry a translation to the right of the first.
	assert.Contains(t, string(actual), `<g transform="translate(130 0)">`)
}

func TestComposeIntoExistingCanvas(t *testing.T) {
	canvas := svg.New().WidthHeight(300, 40, svg.Number).AppendChildren(
		svg.Rect().ID("badge-gradient").XYWidthHeight(0, 0, 10, 10, svg.Number),
	)
	badge := badgeDocument(t, "alpha", "#123456", "#654321")

	if _, err := svg.ComposeInto(canvas, svg.Placement{Document: badge}); err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, 2, len(canvas.Children))
	written := canvas.String()
	assert.Contains(t, written, `id="badge-gradient"`, "the pre-existing ID is untouched")
	assert.Contains(t, written, `id="alpha-1-badge-gradient"`)
	assert.Contains(t, written, `fill="url(#alpha-1-badge-gradient)"`)
}

func TestComposeIntoReservesNestedIDs(t *testing.T) {
	// IDs anywhere in existing canvas content, including inside defs and
	// nested groups, must be reserved before new IDs are chosen.
	canvas := svg.New().AppendChildren(
		svg.Defs(svg.LinearGradient().ID("badge-gradient")),
		svg.G(svg.Circle().ID("badge-clip")),
	)
	badge := badgeDocument(t, "alpha", "#123456", "#654321")

	if _, err := svg.ComposeInto(canvas, svg.Placement{Document: badge}); err != nil {
		t.Fatal(err)
	}
	ids, _ := documentIDsAndReferences(t, []byte(canvas.String()))
	assert.Equal(t, len(ids), len(uniqueStrings(ids)))
	assert.Contains(t, canvas.String(), `id="alpha-1-badge-gradient"`)
	assert.Contains(t, canvas.String(), `id="alpha-1-badge-clip"`)
}

func TestComposeSameDocumentTwice(t *testing.T) {
	badge := badgeDocument(t, "alpha", "#123456", "#654321")
	canvas, err := svg.Compose(
		svg.Placement{Document: badge},
		svg.Placement{Document: badge}.At(130, 0),
	)
	assert.NoError(t, err)
	ids, references := documentIDsAndReferences(t, []byte(canvas.String()))
	assert.Equal(t, len(ids), len(uniqueStrings(ids)))
	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	for reference := range references {
		_, ok := idSet[reference]
		assert.True(t, ok, "reference #%s has no definition", reference)
	}
	assert.Contains(t, canvas.String(), `id="alpha-2-badge-gradient"`)
}

func TestComposeKeepsDocumentViewport(t *testing.T) {
	// Without a viewBox, a document's width and height still define the
	// viewport that its percentage lengths resolve against.
	document := svg.NewDocument("percent", svg.New().
		WidthHeight(120, 40, svg.Number).
		AppendChildren(svg.Rect().WidthHeight(100, 100, svg.Percent)))
	canvas, err := svg.Compose(svg.Placement{Document: document})
	assert.NoError(t, err)
	assert.Contains(t, canvas.String(), `<svg height="40" overflow="hidden" width="120">`)
	assert.Contains(t, canvas.String(), `width="100%"`)
}

func TestComposePlacementScaled(t *testing.T) {
	badge := badgeDocument(t, "alpha", "#123456", "#654321")
	canvas, err := svg.Compose(
		svg.Placement{Document: badge}.At(10, 20).WithScale(2, 3),
	)
	assert.NoError(t, err)
	// The transform list is applied right to left: the document is scaled
	// first, then translated.
	assert.Contains(t, canvas.String(), `transform="translate(10 20) scale(2 3)"`)
}

func TestComposeNestedDefinitions(t *testing.T) {
	source := svg.New().
		ViewBox(0, 0, 100, 100).
		AppendChildren(
			svg.Defs(
				svg.Symbol().ID("icon").ViewBox(0, 0, 10, 10).AppendChildren(
					svg.Path().D(svgpath.New().
						MoveToAbs([]float64{0, 0}).
						LineToAbs([]float64{10, 10}),
					).Fill("url(#paint)"),
				),
				svg.LinearGradient().ID("paint").AppendChildren(
					svg.Stop().Offset("0").StopColor("red"),
					svg.Stop().Offset("1").StopColor("blue"),
				),
				svg.ClipPath().ID("round").AppendChildren(
					svg.Circle().CXCYR(5, 5, 5, svg.Number),
				),
			),
			svg.Use().Href("#icon").ClipPath("url(#round)"),
		)
	document := svg.NewDocument("alpha", source)

	canvas, err := svg.Compose(
		svg.Placement{Document: document},
		svg.Placement{Document: document}.At(110, 0),
	)
	assert.NoError(t, err)
	written := canvas.String()

	// Every copy's use, clip path, and gradient chain must be self-consistent
	// after renaming, including the gradient nested inside the symbol. The
	// first copy keeps its IDs; only the second copy's colliding IDs move.
	assert.Contains(t, written, `<use clip-path="url(#round)" href="#icon"></use>`)
	assert.Contains(t, written, `<use clip-path="url(#alpha-2-round)" href="#alpha-2-icon"></use>`)
	assert.Contains(t, written, `<path d="M0,0 L10,10" fill="url(#paint)"></path>`)
	assert.Contains(t, written, `<path d="M0,0 L10,10" fill="url(#alpha-2-paint)"></path>`)
	assert.Contains(t, written, `<linearGradient id="paint">`)
	assert.Contains(t, written, `<linearGradient id="alpha-2-paint">`)
	assert.Contains(t, written, `<clipPath id="alpha-2-round">`)
}

func TestComposeKeepsTransitiveDefs(t *testing.T) {
	document := svg.NewDocument("alpha", svg.New().AppendChildren(
		svg.Defs(
			svg.LinearGradient().ID("base").AppendChildren(
				svg.Stop().Offset("0").StopColor("red"),
			),
			svg.LinearGradient().ID("derived").Href("#base"),
			svg.LinearGradient().ID("dead").AppendChildren(
				svg.Stop().Offset("0").StopColor("blue"),
			),
		),
		svg.Rect().Fill("url(#derived)"),
	))
	canvas, err := svg.Compose(svg.Placement{Document: document})
	assert.NoError(t, err)
	written := canvas.String()
	assert.Contains(t, written, `id="base"`, "a defs entry reached only through href is kept")
	assert.Contains(t, written, `id="derived"`)
	assert.Contains(t, written, `href="#base"`)
	assert.NotContains(t, written, `id="dead"`)
}

func TestComposeStyleRewrites(t *testing.T) {
	source := svg.New().ViewBox(0, 0, 10, 10).AppendChildren(
		svg.Defs(
			svg.LinearGradient().ID("paint").AppendChildren(
				svg.Stop().Offset("0").StopColor("red"),
			),
		),
		svg.Style().Type("text/css").AppendChildren(
			svg.CharData("#painted { fill: url(#paint) }\n#painted\t{ stroke: none }"),
		),
		svg.Rect().ID("painted").XYWidthHeight(0, 0, 10, 10, svg.Number).
			Style("opacity: 1; fill: url(#paint)"),
	)
	document := svg.NewDocument("alpha", source)

	canvas, err := svg.Compose(
		svg.Placement{Document: document},
		svg.Placement{Document: document}.At(20, 0),
	)
	assert.NoError(t, err)
	written := canvas.String()

	assert.Contains(t, written, "#alpha-2-painted { fill: url(#alpha-2-paint) }")
	assert.Contains(t, written, "#alpha-2-painted&#x9;{ stroke: none }")
	assert.Contains(t, written, `style="opacity: 1; fill: url(#alpha-2-paint)"`)
}

func TestComposeErrors(t *testing.T) {
	badViewBox := svg.NewDocument("bad-viewbox", svg.New().ViewBox(0, 0, 0, 10))
	negativeViewBox := svg.NewDocument("negative-viewbox", svg.New().ViewBox(0, 0, -1, 10))
	duplicateID := svg.NewDocument("duplicate", svg.New().AppendChildren(
		svg.Rect().ID("shape").XYWidthHeight(0, 0, 1, 1, svg.Number),
		svg.Circle().ID("shape").CXCYR(0, 0, 1, svg.Number),
	))
	unresolved := svg.NewDocument("unresolved", svg.New().AppendChildren(
		svg.Rect().Fill("url(#missing)"),
	))
	unresolvedHref := svg.NewDocument("unresolved-href", svg.New().AppendChildren(
		svg.Use().Href("#missing"),
	))
	external := svg.NewDocument("external", svg.New().AppendChildren(
		svg.Defs(svg.LinearGradient().ID("paint")),
		svg.Rect().Fill("url(../shared.svg#paint)"),
	))
	externalHref := svg.NewDocument("external-href", svg.New().AppendChildren(
		svg.Use().Href("other.svg#paint"),
	))
	imported := svg.NewDocument("imported", svg.New().AppendChildren(
		svg.Style().AppendChildren(svg.CharData("@import url(theme.css);")),
	))
	circularGradient := svg.NewDocument("circular", svg.New().AppendChildren(
		svg.Defs(
			svg.LinearGradient().ID("a").Href("#b"),
			svg.LinearGradient().ID("b").Href("#a"),
			svg.Rect().Fill("url(#a)"),
		),
	))
	circularUse := svg.NewDocument("circular-use", svg.New().AppendChildren(
		svg.Defs(svg.G().ID("a").AppendChildren(svg.Use().Href("#a"))),
		svg.Use().Href("#a"),
	))
	malformedViewBox := svg.NewDocument("malformed-viewbox", svg.New())
	malformedViewBox.Root.Attrs["viewBox"] = svg.String("0 0 10")

	for _, tc := range []struct {
		name     string
		document *svg.Document
		want     error
	}{
		{"bad-viewbox", badViewBox, svg.ErrInvalidViewBox},
		{"negative-viewbox", negativeViewBox, svg.ErrInvalidViewBox},
		{"malformed-viewbox", malformedViewBox, svg.ErrInvalidViewBox},
		{"duplicate-id", duplicateID, svg.ErrAmbiguousID},
		{"unresolved-url", unresolved, svg.ErrUnresolvedReference},
		{"unresolved-href", unresolvedHref, svg.ErrUnresolvedReference},
		{"external-url", external, svg.ErrExternalReference},
		{"external-href", externalHref, svg.ErrExternalReference},
		{"import", imported, svg.ErrExternalReference},
		{"circular", circularGradient, svg.ErrCircularReference},
		{"circular-use", circularUse, svg.ErrCircularReference},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canvas, err := svg.Compose(svg.Placement{Document: tc.document})
			assert.IsError(t, err, tc.want)
			assert.Zero(t, canvas, "all-or-nothing: no partial canvas")
		})
	}
}

func TestComposeRejectsBadPlacement(t *testing.T) {
	badge := badgeDocument(t, "alpha", "#123456", "#654321")
	canvas, err := svg.Compose(svg.Placement{Document: badge}.At(math.Inf(1), 0))
	assert.IsError(t, err, svg.ErrInvalidPlacement)
	assert.Zero(t, canvas)
}

func TestComposeRejectsNoDocuments(t *testing.T) {
	_, err := svg.Compose()
	assert.IsError(t, err, svg.ErrInvalidDocument)
}

func TestComposeRejectsNilDocument(t *testing.T) {
	_, err := svg.Compose(svg.Placement{})
	assert.IsError(t, err, svg.ErrInvalidDocument)
}

func TestComposeLeavesCanvasUnchangedOnError(t *testing.T) {
	canvas := svg.New().AppendChildren(
		svg.Rect().ID("existing").XYWidthHeight(0, 0, 1, 1, svg.Number),
	)
	before := canvas.String()

	use := svg.Use()
	use.Attrs["xlink:href"] = svg.String("#a")
	good := svg.NewDocument("good", svg.New().AppendChildren(svg.Rect().ID("a")))
	bad := svg.NewDocument("bad", svg.New().AppendChildren(
		svg.Rect().Fill("url(#missing)"),
	))
	_, err := svg.ComposeInto(canvas, svg.Placement{Document: good}, svg.Placement{Document: bad})
	assert.IsError(t, err, svg.ErrUnresolvedReference)
	assert.Equal(t, before, canvas.String(), "a rejected placement must leave the canvas untouched")
	_, declared := canvas.Attrs["xmlns:xlink"]
	assert.False(t, declared, "namespaces are applied only on success")
}

func TestComposeDocumentReusableAfterError(t *testing.T) {
	document := svg.NewDocument("alpha", svg.New().AppendChildren(
		svg.Rect().ID("shape").Fill("url(#missing)"),
		svg.Defs(svg.LinearGradient().ID("paint")),
	))
	_, err := svg.Compose(svg.Placement{Document: document})
	assert.IsError(t, err, svg.ErrUnresolvedReference)
	// The rejected document must still write exactly as authored.
	assert.Equal(t, `<svg version="1.1" xmlns="http://www.w3.org/2000/svg"><rect fill="url(#missing)" id="shape"></rect><defs><linearGradient id="paint"></linearGradient></defs></svg>`, document.Root.String())
}

func TestComposeAllowsHyperlink(t *testing.T) {
	// An <a> href is navigation, not an embedded resource, so an external URL
	// is legitimate and must be preserved verbatim.
	document := svg.NewDocument("link", svg.New().AppendChildren(
		svg.A().Href("https://example.com/").AppendChildren(
			svg.Text().AppendChildren(svg.CharData("example")),
		),
	))
	canvas, err := svg.Compose(svg.Placement{Document: document})
	assert.NoError(t, err)
	assert.Contains(t, canvas.String(), `href="https://example.com/"`)
}

func TestComposeNamespaceConflict(t *testing.T) {
	source := svg.New().AppendChildren(
		rectWithXLinkHref("#paint"),
		svg.Rect().ID("paint"),
	)
	document := svg.NewDocument("alpha", source)
	canvas := svg.New()
	canvas.Attrs["xmlns:xlink"] = svg.String("http://example.com/other")

	_, err := svg.ComposeInto(canvas, svg.Placement{Document: document})
	assert.IsError(t, err, svg.ErrNamespaceConflict)
}

func TestComposeXLinkNamespace(t *testing.T) {
	use := svg.Use()
	use.Attrs["xlink:href"] = svg.String("#paint")
	document := svg.NewDocument("alpha", svg.New().AppendChildren(
		use,
		svg.Rect().ID("paint"),
	))
	canvas, err := svg.Compose(svg.Placement{Document: document})
	assert.NoError(t, err)
	assert.Equal(t, "http://www.w3.org/1999/xlink", canvas.Attrs["xmlns:xlink"].String())
	assert.Contains(t, canvas.String(), `<use xlink:href="#paint"></use>`)
}

// documentIDsAndReferences returns all id attribute values and all local
// references in data.
func documentIDsAndReferences(t *testing.T, data []byte) ([]string, map[string]struct{}) {
	t.Helper()
	var ids []string
	references := make(map[string]struct{})
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		assert.NoError(t, err)
		startElement, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range startElement.Attr {
			if attr.Name.Local == "id" {
				ids = append(ids, attr.Value)
				continue
			}
			if attr.Name.Local == "href" && strings.HasPrefix(attr.Value, "#") {
				references[strings.TrimPrefix(attr.Value, "#")] = struct{}{}
			}
			for _, part := range strings.Split(attr.Value, "url(#")[1:] {
				references[strings.SplitN(part, ")", 2)[0]] = struct{}{}
			}
		}
	}
	return ids, references
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func rectWithXLinkHref(href string) *svg.RectElement {
	rect := svg.Rect()
	rect.Attrs["xlink:href"] = svg.String(href)
	return rect
}
