package svg_test

import (
	"os"

	"github.com/twpayne/go-svg"
)

// badge returns a small badge whose gradient and clip path use fixed IDs.
func badge(name, from, to string) *svg.Document {
	return svg.NewDocument(name, svg.New().
		ViewBox(0, 0, 120, 40).
		AppendChildren(
			svg.Defs(
				svg.LinearGradient().ID("badge-fill").
					X1("0").Y1("0").X2("0").Y2("1").
					AppendChildren(
						svg.Stop().Offset("0").StopColor(svg.String(from)),
						svg.Stop().Offset("1").StopColor(svg.String(to)),
					),
			),
			svg.Rect().XYWidthHeight(0, 0, 120, 40, svg.Number).Fill("url(#badge-fill)"),
		))
}

func ExampleCompose() {
	canvas, err := svg.Compose(
		svg.Placement{Document: badge("first", "#f00", "#00f")},
		svg.Placement{Document: badge("second", "#0f0", "#ff0")}.At(130, 0),
	)
	if err != nil {
		panic(err)
	}
	if _, err := canvas.WriteToIndent(os.Stdout, "", "  "); err != nil {
		panic(err)
	}

	// Output:
	// <svg version="1.1" xmlns="http://www.w3.org/2000/svg">
	//   <svg height="40" overflow="hidden" viewBox="0 0 120 40" width="120">
	//     <defs>
	//       <linearGradient id="badge-fill" x1="0" x2="0" y1="0" y2="1">
	//         <stop offset="0" stop-color="#f00"></stop>
	//         <stop offset="1" stop-color="#00f"></stop>
	//       </linearGradient>
	//     </defs>
	//     <rect fill="url(#badge-fill)" height="40" width="120" x="0" y="0"></rect>
	//   </svg>
	//   <g transform="translate(130 0)">
	//     <svg height="40" overflow="hidden" viewBox="0 0 120 40" width="120">
	//       <defs>
	//         <linearGradient id="second-2-badge-fill" x1="0" x2="0" y1="0" y2="1">
	//           <stop offset="0" stop-color="#0f0"></stop>
	//           <stop offset="1" stop-color="#ff0"></stop>
	//         </linearGradient>
	//       </defs>
	//       <rect fill="url(#second-2-badge-fill)" height="40" width="120" x="0" y="0"></rect>
	//     </svg>
	//   </g>
	// </svg>
}
