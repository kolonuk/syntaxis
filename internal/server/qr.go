// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/base64"
	"fmt"
	"strings"

	"rsc.io/qr"
)

// qrDataURL draws text as a QR code and returns it as an SVG data: URL for an <img>. It is made on
// the server so the 2FA secret is never handed to a third-party service or a script bundle; the
// page's content security policy already allows data: images.
func qrDataURL(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4 // modules of white around the code, which scanners need
	n := code.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, n, n)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(b.String())), nil
}
