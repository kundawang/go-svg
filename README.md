# go-svg

[![PkgGoDev](https://pkg.go.dev/badge/github.com/twpayne/go-svg)](https://pkg.go.dev/github.com/twpayne/go-svg)

Package `svg` provides convenience methods for creating and writing SVG documents.

## Key Features

* Fluent API for building arbitrarily complex SVG documents.
* Support for all SVG elements.
* Support for all SVG units.
* Compatibility with the standard library's [`encoding/xml`](https://pkg.go.dev/encoding/xml) package.
* Simple mapping between functions and SVG elements.

## Example

```go
func ExampleNew() {
    svgElement := svg.New().WidthHeight(4, 4, svg.CM).ViewBox(0, 0, 400, 400).AppendChildren(
        svg.Title(svg.CharData("Example triangle01- simple example of a 'path'")),
        svg.Desc(svg.CharData("A path that draws a triangle")),
        svg.Rect().XYWidthHeight(1, 1, 398, 398, svg.Number).Fill("none").Stroke("blue"),
        svg.Path().D(svgpath.New().
            MoveToAbs([]float64{100, 100}).
            LineToAbs([]float64{300, 100}).
            LineToAbs([]float64{200, 300}).
            ClosePath(),
        )).Fill("red").Stroke("blue").StrokeWidth(svg.Number(3)),
    )
    if _, err := svgElement.WriteToIndent(os.Stdout, "", "  "); err != nil {
        panic(err)
    }
}
```

Output:

```xml
<svg height="4cm" version="1.1" viewBox="0 0 400 400" width="4cm" xmlns="http://www.w3.org/2000/svg">
  <title>Example triangle01- simple example of a &#39;path&#39;</title>
  <desc>A path that draws a triangle</desc>
  <rect fill="none" height="398" stroke="blue" width="398" x="1" y="1"></rect>
  <path d="M100,100 L300,100 L200,300 z" fill="red" stroke="blue" stroke-width="3"></path>
</svg>
```

![Output](testdata/triangle01.svg)

## Composing documents

`Compose` places independent SVG documents on one canvas. Each document is
copied, never mutated, so the same `Document` can be placed repeatedly. IDs
that collide with an ID already on the canvas are renamed and every reference
to them (`href`, `url(#...)` in attributes and `<style>` content, and CSS ID
selectors) is rewritten, so two documents that both define `#badge-gradient`
keep their own gradients.

```go
first := svg.NewDocument("first", svg.New().ViewBox(0, 0, 120, 40).AppendChildren(/* ... */))
second := svg.NewDocument("second", svg.New().ViewBox(0, 0, 120, 40).AppendChildren(/* ... */))

canvas, err := svg.Compose(
    svg.Placement{Document: first},
    svg.Placement{Document: second}.At(130, 0),
)
if err != nil {
    panic(err)
}
```

A document that defines a viewport (a `viewBox`, or a width and a height) is
wrapped in a nested `<svg>` element, so its user coordinates, percentage
lengths, `preserveAspectRatio`, and viewport clipping are preserved. A
`<defs>` element keeps only the entries reachable from rendered content,
following `href` and `url(#...)` chains.

`Compose` is strict: it rejects `viewBox` values that are not four finite
numbers with positive width and height (`ErrInvalidViewBox`), documents that
define the same ID twice (`ErrAmbiguousID`), references to undefined IDs
(`ErrUnresolvedReference`), reference cycles (`ErrCircularReference`), and
references to resources outside the document (`ErrExternalReference`). On any
error it returns a nil canvas and leaves an existing canvas untouched. Use
`ComposeInto` to add to an existing canvas.

## Dynamic example

See [`cmd/svgclock`](https://github.com/twpayne/go-svg/tree/master/cmd/svgclock).

## License

MIT
