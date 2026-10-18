package phone

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// quietZone is the margin the QR standard demands around the code itself.
const quietZone = 4

// QRSVG answers text as one QR code in SVG — the picture the dashboard shows for the same link
// the terminal prints, black on a white plate so it scans from a screen.
func QRSVG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, err
	}
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", quietZone+x, quietZone+y)
			}
		}
	}
	dim := code.Size + 2*quietZone
	return []byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`+
			`<rect width="%d" height="%d" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		dim, dim, dim, dim, path.String())), nil
}
