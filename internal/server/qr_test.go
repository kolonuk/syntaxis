// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pquerna/otp/totp"
	"rsc.io/qr"
)

// decodeSVG reads the dark modules back out of the picture the server made.
func decodeSVG(t *testing.T, dataURL string) (size int, dark map[[2]int]bool) {
	t.Helper()
	raw, ok := strings.CutPrefix(dataURL, "data:image/svg+xml;base64,")
	if !ok {
		t.Fatalf("not an SVG data URL: %.40s", dataURL)
	}
	svg, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	vb := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(string(svg))
	if vb == nil || vb[1] != vb[2] {
		t.Fatalf("no square viewBox: %s", svg[:80])
	}
	n, _ := strconv.Atoi(vb[1])
	dark = map[[2]int]bool{}
	for _, m := range regexp.MustCompile(`M(\d+) (\d+)h1v1h-1z`).FindAllStringSubmatch(string(svg), -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		dark[[2]int{x, y}] = true
	}
	return n, dark
}

// The picture is the code the library made, module for module, with a white margin, so a scanner
// reads exactly the otpauth link the key was made from.
func TestQRPictureMatchesTheEncodedCodeModuleForModule(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Syntaxis", AccountName: "boss@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := qrDataURL(key.URL())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := qr.Encode(key.URL(), qr.M)
	n, dark := decodeSVG(t, img)
	const quiet = 4
	if n != want.Size+2*quiet {
		t.Fatalf("size %d, want %d plus a margin", n, want.Size)
	}
	for y := 0; y < want.Size; y++ {
		for x := 0; x < want.Size; x++ {
			if dark[[2]int{x + quiet, y + quiet}] != want.Black(x, y) {
				t.Fatalf("module %d,%d differs from the encoded code", x, y)
			}
		}
	}
	for k := range dark {
		if k[0] < quiet || k[1] < quiet || k[0] >= quiet+want.Size || k[1] >= quiet+want.Size {
			t.Fatalf("a dark module %v sits in the margin", k)
		}
	}
}

// Beginning 2FA setup returns the QR picture with the key and link, for admin and user alike.
func TestTwoFactorSetupReturnsAQRCode(t *testing.T) {
	e := newEnv(t)
	// a chat user without 2FA
	u := e.userSession("qr@example.com")
	code, m, raw := u.do("POST", "/api/2fa/begin", nil)
	if code != 200 {
		t.Fatalf("begin: %d %s", code, raw)
	}
	secret, link, img := m["secret"].(string), m["url"].(string), m["qr"].(string)
	if secret == "" || !strings.HasPrefix(link, "otpauth://totp/") || !strings.Contains(link, secret) {
		t.Fatalf("secret %q link %q", secret, link)
	}
	n, dark := decodeSVG(t, img)
	want, _ := qr.Encode(link, qr.M)
	if n != want.Size+8 || len(dark) == 0 {
		t.Fatalf("the picture is not the code for the link: %d modules, %d dark", n, len(dark))
	}
}
