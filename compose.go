package svg

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Sentinel errors returned by [Compose]. Every error returned by Compose wraps
// one of these sentinels, so callers can match them with [errors.Is].
var (
	// ErrInvalidDocument is returned when a document cannot be decoded as a
	// well-formed SVG document.
	ErrInvalidDocument = errors.New("invalid SVG document")
	// ErrInvalidViewBox is returned when a document has a viewBox attribute
	// that is not four finite numbers with positive width and height.
	ErrInvalidViewBox = errors.New("invalid viewBox")
	// ErrAmbiguousID is returned when one document defines the same non-empty
	// ID more than once.
	ErrAmbiguousID = errors.New("duplicate ID")
	// ErrUnresolvedReference is returned when a local reference such as
	// href="#x" or url(#x) names an ID that the document does not define.
	ErrUnresolvedReference = errors.New("unresolved reference")
	// ErrCircularReference is returned when local references form a cycle,
	// for example a gradient that refers back to itself through href.
	ErrCircularReference = errors.New("circular reference")
	// ErrExternalReference is returned when a document depends on a resource
	// outside itself, such as href="other.svg#x", url(http://...), or an
	// @import rule. Composition only embeds self-contained documents.
	ErrExternalReference = errors.New("external reference")
	// ErrInvalidPlacement is returned when a placement has a non-finite
	// coordinate or scale.
	ErrInvalidPlacement = errors.New("invalid placement")
	// ErrNamespaceConflict is returned when a placed document uses a
	// namespace prefix that the destination canvas already maps to a
	// different URI.
	ErrNamespaceConflict = errors.New("namespace conflict")
)

const (
	xmlnsPrefix    = "xmlns"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
	styleElement   = "style"
	hrefAttribute  = "href"
)

// A Document is a self-contained SVG document that can be placed on a
// composed canvas.
//
// Compose never mutates a Document's element tree, so the same Document may be
// composed any number of times.
type Document struct {
	// Name identifies the document in error messages and supplies the prefix
	// used when renaming its IDs. An empty name is treated as "doc".
	Name string
	// Root is the document's root svg element.
	Root *SVGElement
}

// NewDocument returns a new Document named name with root.
func NewDocument(name string, root *SVGElement) *Document {
	return &Document{
		Name: name,
		Root: root,
	}
}

// A Placement places a Document on a canvas. Its coordinates are in the
// destination canvas's user units; the document's own width and height
// attributes are not interpreted.
type Placement struct {
	// Document is the document to place.
	Document *Document
	// X and Y translate the document's origin on the canvas.
	X, Y float64
	// ScaleX and ScaleY scale the document's user coordinates. A zero value
	// means one. Negative values mirror the document about the corresponding
	// axis.
	ScaleX, ScaleY float64
}

// At returns p translated to (x, y).
func (p Placement) At(x, y float64) Placement {
	p.X = x
	p.Y = y
	return p
}

// WithScale returns p scaled by scaleX and scaleY.
func (p Placement) WithScale(scaleX, scaleY float64) Placement {
	p.ScaleX = scaleX
	p.ScaleY = scaleY
	return p
}

// Compose places documents on a new SVG canvas and returns it. It is
// equivalent to ComposeInto(nil, placements...).
func Compose(placements ...Placement) (*SVGElement, error) {
	return ComposeInto(nil, placements...)
}

// ComposeInto places documents on canvas and returns canvas.
//
// Each placed document becomes a <g> element carrying the placement's
// transform. A document that defines a viewport (a viewBox, or a width and a
// height) is wrapped in a nested <svg> element that reproduces that viewport,
// so its user coordinates, percentage lengths, preserveAspectRatio, and
// clipping are unchanged.
//
// IDs defined by a placed document keep their names unless they collide with
// an ID already on the canvas, in which case they are prefixed with the
// document's name and placement index. All local references, including href,
// url(#...) in attributes, and url(#...) inside <style> content, are rewritten
// to match, and inline <style> ID selectors are rewritten too. A <defs>
// element retains only the entries reachable from rendered content.
//
// A nil canvas is replaced by [New]. Existing canvas children are left
// untouched, and their IDs are reserved so that generated IDs cannot collide
// with them.
//
// ComposeInto is all-or-nothing: if any document is rejected, it returns a nil
// canvas and an error wrapping one of the sentinel errors above, and canvas is
// left unchanged.
func ComposeInto(canvas *SVGElement, placements ...Placement) (*SVGElement, error) {
	if len(placements) == 0 {
		return nil, fmt.Errorf("%w: no documents to compose", ErrInvalidDocument)
	}
	if canvas == nil {
		canvas = New()
	}
	composer := newComposer(canvas)
	groups := make([]Element, 0, len(placements))
	for index, placement := range placements {
		group, err := composer.place(index, placement)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	// Apply everything only once all documents have been placed, so a
	// rejected document leaves canvas untouched.
	if len(composer.pendingNamespaces) != 0 {
		if canvas.Attrs == nil {
			canvas.Attrs = make(map[string]AttrValue)
		}
		maps.Copy(canvas.Attrs, composer.pendingNamespaces)
	}
	canvas.Children = append(canvas.Children, groups...)
	return canvas, nil
}

// A composer holds state shared by all placements in one Compose call.
type composer struct {
	canvas            *SVGElement
	usedID            map[string]struct{}
	pendingNamespaces map[string]AttrValue
}

func newComposer(canvas *SVGElement) *composer {
	composer := &composer{
		canvas:            canvas,
		usedID:            make(map[string]struct{}),
		pendingNamespaces: make(map[string]AttrValue),
	}
	if len(canvas.Children) != 0 {
		if root, err := decodeSVGElement(canvas); err == nil {
			root.each(func(element *decodedElement) {
				if id := element.id(); id != "" {
					composer.usedID[id] = struct{}{}
				}
			})
		}
	}
	return composer
}

func (c *composer) place(index int, placement Placement) (Element, error) {
	document := placement.Document
	if document == nil || document.Root == nil {
		return nil, fmt.Errorf("%w: placement %d has no document", ErrInvalidDocument, index)
	}
	name := document.Name
	if name == "" {
		name = "doc"
	}
	if err := validatePlacement(placement); err != nil {
		return nil, fmt.Errorf("document %q: %w", name, err)
	}
	root, err := decodeSVGElement(document.Root)
	if err != nil {
		return nil, fmt.Errorf("document %q: %w", name, err)
	}
	if root.prefix == "" && root.name != "svg" {
		return nil, fmt.Errorf("%w: document %q: root element is %q, want \"svg\"", ErrInvalidDocument, name, root.name)
	}
	viewBox, hasViewBox, err := parseDocumentViewBox(name, root)
	if err != nil {
		return nil, err
	}

	// A document is validated exactly as authored, before unreferenced defs
	// entries are pruned, so a malformed document is always reported.
	byID, elements, err := indexIDs(name, root)
	if err != nil {
		return nil, err
	}
	if err := validateReferences(name, root, byID); err != nil {
		return nil, err
	}
	if err := checkReferenceCycles(name, byID, elements); err != nil {
		return nil, err
	}
	pruneDefs(root, byID)

	// Only IDs that collide with one already on the canvas are renamed, so
	// single-document output keeps its original IDs; placing the same
	// Document twice still gives each copy its own definitions.
	rewriteTree(root, c.renameConflictingIDs(documentIDPrefix(name, index), root))
	if err := c.addNamespaces(name, root); err != nil {
		return nil, err
	}

	inner := &rawElement{
		name:     "g",
		children: elementsToElements(rootChildren(root)),
	}
	if preserveAspectRatio, ok := root.attr("preserveAspectRatio"); ok {
		inner.attrs = map[string]AttrValue{"preserveAspectRatio": String(preserveAspectRatio)}
	}
	// Presentation attributes and class/style on the source root apply to its
	// rendered content, so carry them onto the wrapper. x, y, and version
	// describe the standalone viewport position and are not carried.
	for attributeName, value := range documentRootAttributes(root) {
		if inner.attrs == nil {
			inner.attrs = make(map[string]AttrValue)
		}
		inner.attrs[attributeName] = value
	}
	if hasViewport(root) {
		// A nested svg element reproduces the source document's viewport,
		// viewBox, preserveAspectRatio, and clipping exactly, so percentage
		// lengths and the user coordinate system are unchanged.
		inner.name = "svg"
		inner.attrs = documentViewportAttributes(root, viewBox, hasViewBox, inner.attrs)
	}
	if id := root.id(); id != "" {
		if inner.attrs == nil {
			inner.attrs = make(map[string]AttrValue)
		}
		inner.attrs["id"] = String(id)
	}

	transform := placementTransform(placement)
	if transform == "" {
		return inner, nil
	}
	return &rawElement{
		name:     "g",
		attrs:    map[string]AttrValue{"transform": String(transform)},
		children: []Element{inner},
	}, nil
}

// hasViewport reports whether root describes a viewport of its own, by having
// a viewBox or both a width and a height. A document without one is placed as
// a plain group.
func hasViewport(root *decodedElement) bool {
	_, widthOK := root.attr("width")
	_, heightOK := root.attr("height")
	_, viewBoxOK := root.attr("viewBox")
	return viewBoxOK || widthOK && heightOK
}

// documentViewportAttributes returns attrs augmented with the attributes of a
// nested viewport that reproduces root. The source's width and height are kept
// when given because percentage lengths in the document resolve against them;
// otherwise they are taken from the viewBox.
func documentViewportAttributes(root *decodedElement, viewBox ViewBox, hasViewBox bool, attrs map[string]AttrValue) map[string]AttrValue {
	if attrs == nil {
		attrs = make(map[string]AttrValue)
	}
	for _, name := range []string{"width", "height"} {
		if value, ok := root.attr(name); ok && value != "" {
			attrs[name] = String(value)
		}
	}
	if hasViewBox {
		attrs["viewBox"] = viewBox
		if _, ok := attrs["width"]; !ok {
			attrs["width"] = Number(viewBox.Width)
		}
		if _, ok := attrs["height"]; !ok {
			attrs["height"] = Number(viewBox.Height)
		}
	}
	if _, ok := attrs["overflow"]; !ok {
		attrs["overflow"] = String("hidden")
	}
	return attrs
}

func validatePlacement(placement Placement) error {
	values := []struct {
		name  string
		value float64
	}{
		{"x", placement.X},
		{"y", placement.Y},
		{"scaleX", placement.ScaleX},
		{"scaleY", placement.ScaleY},
	}
	for _, value := range values {
		if math.IsNaN(value.value) || math.IsInf(value.value, 0) {
			return fmt.Errorf("%w: non-finite %s", ErrInvalidPlacement, value.name)
		}
	}
	return nil
}

func placementScale(placement Placement) (float64, float64) {
	scaleX, scaleY := placement.ScaleX, placement.ScaleY
	if scaleX == 0 {
		scaleX = 1
	}
	if scaleY == 0 {
		scaleY = 1
	}
	return scaleX, scaleY
}

// placementTransform returns the transform that maps the document's user
// coordinates into the canvas. In a transform list the rightmost function is
// applied first, so translation is written first to scale about (X, Y) after
// the document has been scaled about its own origin.
func placementTransform(placement Placement) string {
	scaleX, scaleY := placementScale(placement)
	parts := make([]string, 0, 2)
	if placement.X != 0 || placement.Y != 0 {
		parts = append(parts, "translate("+formatNumber(placement.X)+" "+formatNumber(placement.Y)+")")
	}
	if scaleX != 1 || scaleY != 1 {
		parts = append(parts, "scale("+formatNumber(scaleX)+" "+formatNumber(scaleY)+")")
	}
	return strings.Join(parts, " ")
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// A decodedElement is a prefix-preserving copy of an XML element. Prefixes are
// kept verbatim so that composed output round-trips names such as xlink:href
// and foreign markup without encoding/xml reassigning their prefixes.
type decodedElement struct {
	prefix   string
	name     string
	attrs    []decodedAttr
	children []decodedChild
}

type decodedAttr struct {
	prefix string
	name   string
	value  string
}

type decodedChild struct {
	element *decodedElement
	text    string
	isText  bool
}

func decodeSVGElement(root *SVGElement) (*decodedElement, error) {
	var buffer bytes.Buffer
	if err := xml.NewEncoder(&buffer).Encode(root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(buffer.Bytes()))
	var stack []*decodedElement
	var decoded *decodedElement
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 && decoded != nil {
				return nil, fmt.Errorf("%w: multiple root elements", ErrInvalidDocument)
			}
			element := &decodedElement{
				prefix: token.Name.Space,
				name:   token.Name.Local,
			}
			for _, attr := range token.Attr {
				element.attrs = append(element.attrs, decodedAttr{
					prefix: attr.Name.Space,
					name:   attr.Name.Local,
					value:  attr.Value,
				})
			}
			if len(stack) == 0 {
				decoded = element
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, decodedChild{element: element})
			}
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("%w: unmatched end element", ErrInvalidDocument)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			text := string(token)
			if len(stack) == 0 {
				if strings.TrimSpace(text) != "" {
					return nil, fmt.Errorf("%w: character data outside the root element", ErrInvalidDocument)
				}
				continue
			}
			parent := stack[len(stack)-1]
			if n := len(parent.children); n != 0 && parent.children[n-1].isText {
				parent.children[n-1].text += text
				continue
			}
			parent.children = append(parent.children, decodedChild{text: text, isText: true})
		case xml.Comment, xml.ProcInst, xml.Directive:
			// Comments, processing instructions, and directives are not part
			// of the rendered document and are dropped.
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("%w: unclosed element %q", ErrInvalidDocument, stack[len(stack)-1].name)
	}
	if decoded == nil {
		return nil, fmt.Errorf("%w: empty document", ErrInvalidDocument)
	}
	return decoded, nil
}

func (e *decodedElement) qualifiedName() string {
	if e.prefix == "" {
		return e.name
	}
	return e.prefix + ":" + e.name
}

func (e *decodedElement) attr(name string) (string, bool) {
	for _, attr := range e.attrs {
		if attr.prefix == "" && attr.name == name {
			return attr.value, true
		}
	}
	return "", false
}

func (e *decodedElement) id() string {
	value, _ := e.attr("id")
	return value
}

func (e *decodedElement) setAttr(name, value string) {
	for i := range e.attrs {
		if e.attrs[i].prefix == "" && e.attrs[i].name == name {
			e.attrs[i].value = value
			return
		}
	}
	e.attrs = append(e.attrs, decodedAttr{name: name, value: value})
}

// each calls fn for e and every descendant element in document order.
func (e *decodedElement) each(fn func(*decodedElement)) {
	fn(e)
	for _, child := range e.children {
		if !child.isText {
			child.element.each(fn)
		}
	}
}

// walk is like each, but passes the element's path and stops on error.
func (e *decodedElement) walk(path string, visit func(*decodedElement, string) error) error {
	if err := visit(e, path); err != nil {
		return err
	}
	for _, child := range e.children {
		if child.isText {
			continue
		}
		if err := child.element.walk(path+"/"+child.element.qualifiedName(), visit); err != nil {
			return err
		}
	}
	return nil
}

// parseDocumentViewBox returns the document's viewBox and whether it has one.
func parseDocumentViewBox(name string, root *decodedElement) (ViewBox, bool, error) {
	value, ok := root.attr("viewBox")
	if !ok {
		return ViewBox{}, false, nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) != 4 {
		return ViewBox{}, false, fmt.Errorf("%w: document %q: %q", ErrInvalidViewBox, name, value)
	}
	values := make([]float64, len(fields))
	for i, field := range fields {
		parsed, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return ViewBox{}, false, fmt.Errorf("%w: document %q: %q", ErrInvalidViewBox, name, value)
		}
		values[i] = parsed
	}
	if values[2] <= 0 || values[3] <= 0 {
		return ViewBox{}, false, fmt.Errorf("%w: document %q: non-positive width or height in %q", ErrInvalidViewBox, name, value)
	}
	return ViewBox{MinX: values[0], MinY: values[1], Width: values[2], Height: values[3]}, true, nil
}

// indexIDs returns a map from each defined ID to its element and all elements
// in document order. Duplicate non-empty IDs are an error.
func indexIDs(name string, root *decodedElement) (map[string]*decodedElement, []*decodedElement, error) {
	byID := make(map[string]*decodedElement)
	elements := make([]*decodedElement, 0, 16)
	err := root.walk(root.qualifiedName(), func(element *decodedElement, path string) error {
		elements = append(elements, element)
		id := element.id()
		if id == "" {
			return nil
		}
		if _, exists := byID[id]; exists {
			return fmt.Errorf("%w: document %q: id %q is defined more than once, last at %s", ErrAmbiguousID, name, id, path)
		}
		byID[id] = element
		return nil
	})
	return byID, elements, err
}

func isDefsElement(element *decodedElement) bool {
	return element.prefix == "" && element.name == "defs"
}

// pruneDefs removes entries from every <defs> element that are not reachable
// from rendered content. Reachability follows local references transitively, so
// a gradient inherited through href by a referenced gradient is retained. A
// <defs> element left empty is removed.
func pruneDefs(root *decodedElement, byID map[string]*decodedElement) {
	reachable := make(map[string]struct{})
	queue := localReferences(root)
	var seed func(element *decodedElement)
	seed = func(element *decodedElement) {
		for _, child := range element.children {
			if child.isText || isDefsElement(child.element) {
				continue
			}
			queue = append(queue, localReferences(child.element)...)
			seed(child.element)
		}
	}
	seed(root)
	for len(queue) != 0 {
		id := queue[0]
		queue = queue[1:]
		if _, ok := reachable[id]; ok {
			continue
		}
		target, ok := byID[id]
		if !ok {
			// Unresolved references are reported by validateReferences.
			continue
		}
		reachable[id] = struct{}{}
		target.each(func(element *decodedElement) {
			queue = append(queue, localReferences(element)...)
		})
	}

	root.each(func(element *decodedElement) {
		if !isDefsElement(element) {
			return
		}
		kept := element.children[:0]
		for _, child := range element.children {
			if child.isText {
				if strings.TrimSpace(child.text) != "" {
					kept = append(kept, child)
				}
				continue
			}
			if subtreeReferences(child.element, reachable) {
				kept = append(kept, child)
			}
		}
		element.children = kept
	})

	var removeEmptyDefs func(element *decodedElement)
	removeEmptyDefs = func(element *decodedElement) {
		kept := element.children[:0]
		for _, child := range element.children {
			if child.isText {
				kept = append(kept, child)
				continue
			}
			removeEmptyDefs(child.element)
			if isDefsElement(child.element) && len(child.element.children) == 0 {
				continue
			}
			kept = append(kept, child)
		}
		element.children = kept
	}
	removeEmptyDefs(root)
}

func subtreeReferences(element *decodedElement, reachable map[string]struct{}) bool {
	found := false
	element.each(func(inner *decodedElement) {
		if found {
			return
		}
		if _, ok := reachable[inner.id()]; ok {
			found = true
		}
	})
	return found
}

func documentIDPrefix(name string, index int) string {
	base := sanitizeID(name)
	if base == "" {
		base = "doc"
	}
	return base + "-" + strconv.Itoa(index+1)
}

// renameConflictingIDs prefixes the ID of every element under root whose ID
// already occurs on the canvas, and returns the mapping from renamed to new
// ID. Non-conflicting IDs are left alone, so composing a single document
// reproduces its IDs. The document's own IDs are registered only after the
// whole document has been examined, so two elements in that document are
// compared with the canvas and with earlier documents, not with each other:
// duplicates inside one document have already been rejected.
func (c *composer) renameConflictingIDs(prefix string, root *decodedElement) map[string]string {
	// Collect the document's own IDs first: a generated name must not collide
	// with a literal ID that appears later in the same document.
	own := make(map[string]struct{})
	root.each(func(element *decodedElement) {
		if id := element.id(); id != "" {
			own[id] = struct{}{}
		}
	})
	available := func(id string) bool {
		_, used := c.usedID[id]
		_, ownID := own[id]
		return !used && !ownID
	}

	idMap := make(map[string]string)
	var assigned []string
	root.each(func(element *decodedElement) {
		id := element.id()
		if id == "" {
			return
		}
		if _, used := c.usedID[id]; used && idMap[id] == "" {
			base := prefix + "-" + sanitizeID(id)
			newID := base
			for suffix := 2; ; suffix++ {
				if available(newID) {
					break
				}
				newID = base + "-" + strconv.Itoa(suffix)
			}
			idMap[id] = newID
			element.setAttr("id", newID)
			delete(own, newID)
			assigned = append(assigned, newID)
			return
		}
		assigned = append(assigned, id)
		delete(own, id)
	})
	for _, id := range assigned {
		c.usedID[id] = struct{}{}
	}
	return idMap
}

func sanitizeID(id string) string {
	var builder strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	sanitized := strings.Trim(builder.String(), "-")
	if sanitized == "" {
		return ""
	}
	switch sanitized[0] {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', '-':
		sanitized = "d" + sanitized
	}
	return sanitized
}

// addNamespaces declares on the canvas every namespace prefix that a placed
// document uses. The xlink prefix is given its standard URI when the document
// does not declare it, because the fluent API does not add declarations for
// explicitly prefixed attribute names.
func (c *composer) addNamespaces(name string, root *decodedElement) error {
	declared := make(map[string]string)
	used := make(map[string]struct{})
	addPrefix := func(prefix string) {
		if prefix != "" && prefix != "xml" && prefix != xmlnsPrefix {
			used[prefix] = struct{}{}
		}
	}
	for _, attr := range root.attrs {
		if attr.prefix == xmlnsPrefix {
			declared[attr.name] = attr.value
			continue
		}
		addPrefix(attr.prefix)
	}
	for _, child := range root.children {
		if child.isText {
			continue
		}
		child.element.each(func(element *decodedElement) {
			addPrefix(element.prefix)
			for _, attr := range element.attrs {
				if attr.prefix == xmlnsPrefix {
					declared[attr.name] = attr.value
					continue
				}
				addPrefix(attr.prefix)
			}
		})
	}
	for _, prefix := range sortedKeys(used) {
		uri, ok := declared[prefix]
		if !ok && prefix == "xlink" {
			uri, ok = xlinkNamespace, true
		}
		if !ok {
			return fmt.Errorf("%w: document %q: namespace prefix %q is not declared", ErrInvalidDocument, name, prefix)
		}
		key := "xmlns:" + prefix
		if existing, ok := c.canvas.Attrs[key]; ok {
			if existing.String() != uri {
				return fmt.Errorf("%w: document %q: prefix %q is %q on the canvas but %q in the document", ErrNamespaceConflict, name, prefix, existing.String(), uri)
			}
			continue
		}
		if existing, ok := c.pendingNamespaces[key]; ok {
			if existing.String() != uri {
				return fmt.Errorf("%w: document %q: prefix %q is %q earlier but %q in this document", ErrNamespaceConflict, name, prefix, existing.String(), uri)
			}
			continue
		}
		c.pendingNamespaces[key] = String(uri)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// documentRootAttributes returns the root svg attributes that apply to the
// document's rendered content. Attributes describing a standalone document
// viewport are dropped because placement supplies them.
// documentRootAttributes returns the root svg attributes that apply to the
// document's rendered content. Attributes describing the standalone viewport
// are handled separately by [rootViewportAttributes].
func documentRootAttributes(root *decodedElement) map[string]AttrValue {
	attrs := make(map[string]AttrValue)
	for _, attr := range root.attrs {
		if attr.prefix == xmlnsPrefix {
			continue
		}
		if attr.prefix != "" {
			attrs[attr.prefix+":"+attr.name] = String(attr.value)
			continue
		}
		switch attr.name {
		case xmlnsPrefix, "version", "width", "height", "x", "y", "viewBox",
			"preserveAspectRatio", "id":
			continue
		}
		attrs[attr.name] = String(attr.value)
	}
	return attrs
}

func rootChildren(root *decodedElement) []*decodedElement {
	children := make([]*decodedElement, 0, len(root.children))
	for _, child := range root.children {
		if !child.isText {
			children = append(children, child.element)
		}
	}
	return children
}

// localReferences returns the IDs that element refers to, considering href
// attributes, url(#...) references in any attribute, and <style> content.
func localReferences(element *decodedElement) []string {
	var ids []string
	for _, attr := range element.attrs {
		if isHrefAttribute(attr) {
			if id, ok := fragmentID(attr.value); ok {
				ids = append(ids, id)
			}
			continue
		}
		ids = append(ids, urlReferences(attr.value)...)
	}
	if element.prefix == "" && element.name == styleElement {
		for _, child := range element.children {
			if child.isText {
				ids = append(ids, urlReferences(child.text)...)
			}
		}
	}
	return ids
}

func isHrefAttribute(attr decodedAttr) bool {
	return attr.name == hrefAttribute && (attr.prefix == "" || attr.prefix == "xlink")
}

// fragmentID returns the ID in a value of the form "#id".
func fragmentID(value string) (string, bool) {
	if len(value) < 2 || value[0] != '#' {
		return "", false
	}
	return value[1:], true
}

var cssURLRegexp = regexp.MustCompile(`url\(\s*([^)]*?)\s*\)`)

func urlReferences(value string) []string {
	matches := cssURLRegexp.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil
	}
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if id, ok := fragmentID(strings.Trim(match[1], `"'`)); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func validateReferences(name string, root *decodedElement, byID map[string]*decodedElement) error {
	return root.walk(root.qualifiedName(), func(element *decodedElement, path string) error {
		for _, attr := range element.attrs {
			if err := validateAttributeReference(name, element, path, attr, byID); err != nil {
				return err
			}
		}
		if element.prefix == "" && element.name == styleElement {
			for _, child := range element.children {
				if child.isText {
					if err := validateStyleText(name, path+"/style", child.text, byID); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func validateAttributeReference(name string, element *decodedElement, path string, attr decodedAttr, byID map[string]*decodedElement) error {
	if isHrefAttribute(attr) {
		if attr.value == "" {
			return nil
		}
		if id, ok := fragmentID(attr.value); ok {
			return requireID(name, path+"/@"+attr.name, attr.value, id, byID)
		}
		// An <a> element's href is a navigation link, not an embedded
		// resource, so it may point outside the document.
		if element.prefix == "" && element.name == "a" {
			return nil
		}
		return fmt.Errorf("%w: document %q: %s/@%s %q points outside the document", ErrExternalReference, name, path, attr.name, attr.value)
	}
	for _, match := range cssURLRegexp.FindAllStringSubmatch(attr.value, -1) {
		target := strings.Trim(match[1], `"'`)
		if target == "" {
			continue
		}
		id, ok := fragmentID(target)
		if !ok {
			return fmt.Errorf("%w: document %q: %s/@%s uses %q", ErrExternalReference, name, path, attr.name, target)
		}
		if err := requireID(name, path+"/@"+attr.name, target, id, byID); err != nil {
			return err
		}
	}
	return nil
}

func validateStyleText(name, path, text string, byID map[string]*decodedElement) error {
	if strings.Contains(text, "@import") {
		return fmt.Errorf("%w: document %q: %s uses @import", ErrExternalReference, name, path)
	}
	for _, match := range cssURLRegexp.FindAllStringSubmatch(text, -1) {
		target := strings.Trim(match[1], `"'`)
		if target == "" {
			continue
		}
		id, ok := fragmentID(target)
		if !ok {
			return fmt.Errorf("%w: document %q: %s uses %q", ErrExternalReference, name, path, target)
		}
		if err := requireID(name, path, target, id, byID); err != nil {
			return err
		}
	}
	return nil
}

func requireID(name, path, reference, id string, byID map[string]*decodedElement) error {
	if _, ok := byID[id]; !ok {
		return fmt.Errorf("%w: document %q: %s references %q, which is not defined", ErrUnresolvedReference, name, path, reference)
	}
	return nil
}

// checkReferenceCycles reports the first cycle in document order. An element
// with an ID refers to every ID referenced in its subtree, so a group that
// contains a use of itself is detected as recursive.
func checkReferenceCycles(name string, byID map[string]*decodedElement, elements []*decodedElement) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(byID))
	var visit func(id string, trail []string) error
	visit = func(id string, trail []string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("%w: document %q: %s", ErrCircularReference, name, strings.Join(append(trail, id), " -> "))
		case visited:
			return nil
		}
		state[id] = visiting
		references := make(map[string]struct{})
		byID[id].each(func(element *decodedElement) {
			for _, reference := range localReferences(element) {
				references[reference] = struct{}{}
			}
		})
		for _, reference := range sortedKeys(references) {
			if _, ok := byID[reference]; !ok {
				continue
			}
			if err := visit(reference, append(trail, id)); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for _, element := range elements {
		id := element.id()
		if id == "" || state[id] != unvisited {
			continue
		}
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}

// rewriteTree rewrites every local reference under root using idMap.
func rewriteTree(root *decodedElement, idMap map[string]string) {
	root.each(func(element *decodedElement) {
		for i := range element.attrs {
			attr := &element.attrs[i]
			if isHrefAttribute(*attr) {
				if id, ok := fragmentID(attr.value); ok {
					if newID, ok := idMap[id]; ok {
						attr.value = "#" + newID
					}
				}
				continue
			}
			attr.value = rewriteURLReferences(attr.value, idMap)
		}
		if element.prefix == "" && element.name == styleElement {
			for i := range element.children {
				if !element.children[i].isText {
					continue
				}
				text := rewriteURLReferences(element.children[i].text, idMap)
				for _, id := range sortedKeys(idMap) {
					text = rewriteCSSIDSelector(text, id, idMap[id])
				}
				element.children[i].text = text
			}
		}
	})
}

func rewriteURLReferences(value string, idMap map[string]string) string {
	return cssURLRegexp.ReplaceAllStringFunc(value, func(match string) string {
		inner := strings.Trim(strings.TrimSpace(match[4:len(match)-1]), `"'`)
		id, ok := fragmentID(inner)
		if !ok {
			return match
		}
		newID, ok := idMap[id]
		if !ok {
			return match
		}
		return "url(#" + newID + ")"
	})
}

// rewriteCSSIDSelector replaces the CSS ID selector #id, and only that
// selector: the match must not be followed by another name character.
func rewriteCSSIDSelector(text, id, newID string) string {
	pattern := regexp.MustCompile(`#` + regexp.QuoteMeta(id) + `([^A-Za-z0-9_.\-]|$)`)
	return pattern.ReplaceAllString(text, "#"+newID+"${1}")
}

// rawElement is an element whose name may carry a prefix. It is encoded with
// encodeElement, so composition formats output exactly like the generated
// element types.
type rawElement struct {
	name     string
	attrs    map[string]AttrValue
	children []Element
}

// MarshalXML implements encoding/xml.Marshaler.MarshalXML.
func (e *rawElement) MarshalXML(encoder *xml.Encoder, _ xml.StartElement) error {
	return encodeElement(encoder, e.name, e.attrs, e.children)
}

func (e *decodedElement) toElement() Element {
	var attrs map[string]AttrValue
	if len(e.attrs) != 0 {
		attrs = make(map[string]AttrValue, len(e.attrs))
		for _, attr := range e.attrs {
			name := attr.name
			if attr.prefix != "" {
				name = attr.prefix + ":" + attr.name
			}
			attrs[name] = String(attr.value)
		}
	}
	var children []Element
	for _, child := range e.children {
		if child.isText {
			if child.text != "" {
				children = append(children, CharData(child.text))
			}
			continue
		}
		children = append(children, child.element.toElement())
	}
	return &rawElement{
		name:     e.qualifiedName(),
		attrs:    attrs,
		children: children,
	}
}

func elementsToElements(elements []*decodedElement) []Element {
	result := make([]Element, 0, len(elements))
	for _, element := range elements {
		result = append(result, element.toElement())
	}
	return result
}
