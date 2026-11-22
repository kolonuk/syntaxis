// SPDX-License-Identifier: GPL-2.0-or-later

package uploadcheck

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

var all = map[string]bool{KindImages: true, KindPDF: true, KindDocuments: true, KindText: true}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipBytes(t *testing.T, files map[string]string, order ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	names := order
	for n := range files {
		found := false
		for _, o := range order {
			found = found || o == n
		}
		if !found {
			names = append(names, n)
		}
	}
	for _, n := range names {
		f, _ := w.Create(n)
		_, _ = f.Write([]byte(files[n]))
	}
	_ = w.Close()
	return b.Bytes()
}

func docx(t *testing.T, extra map[string]string) []byte {
	files := map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<w:document>hello</w:document>"}
	for k, v := range extra {
		files[k] = v
	}
	return zipBytes(t, files)
}

func pdf(body string) []byte {
	return []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R " + body + " >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
}

func TestAccepted(t *testing.T) {
	var jb, gb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	_ = gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil)
	odt := zipBytes(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "META-INF/manifest.xml": "<m/>", "content.xml": "<c>hi</c>"}, "mimetype")
	cases := map[string][]byte{
		"a.png": pngBytes(t), "a.jpg": jb.Bytes(), "a.gif": gb.Bytes(),
		"a.pdf":  pdf(""),
		"a.docx": docx(t, nil), "a.xlsx": docx(t, nil), "a.odt": odt,
		"notes.txt": []byte("hello world\n"), "data.csv": []byte("a,b\n1,2\n"), "x.json": []byte(`{"a":1}`), "README.md": []byte("# Title\n---------\n"),
		"line.md": []byte(strings.Repeat("-", 300) + "\n"),
		// ordinary data that merely looks like hex: numbers, and a list of commit hashes
		"numbers.csv": []byte(strings.TrimSuffix(strings.Repeat("12,34,56,78,", 40), ",") + "\n"),
		"hashes.txt":  []byte(strings.Repeat("0123456789abcdef0123456789abcdef01234567\n", 10)),
	}
	for n, d := range cases {
		if err := Check(n, d, all); err != nil {
			t.Errorf("%s should be accepted: %v", n, err)
		}
	}
}

func TestExecutablesAndArchivesRefusedWhateverTheName(t *testing.T) {
	self, err := os.ReadFile(os.Args[0]) // a real program: ELF on Linux
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(self, []byte("\x7fELF")) {
		t.Skip("NOT TESTED: the test binary is not an ELF file on this platform")
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("hello"))
	_ = zw.Close()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	_ = tw.WriteHeader(&tar.Header{Name: "a", Size: 2, Mode: 0o644})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	pe := make([]byte, 0x100)
	copy(pe, "MZ")
	pe[0x3c] = 0x80
	copy(pe[0x80:], "PE\x00\x00")
	for _, name := range []string{"a.png", "a.pdf", "a.txt", "a.docx", "report.pdf.exe", "a", "a.sh", "a.py", "a.zip"} {
		for what, d := range map[string][]byte{"elf": self, "pe": pe, "script": []byte("#!/bin/sh\nrm -rf /\n"), "gzip": gz.Bytes(), "tar": tb.Bytes(), "zip": zipBytes(t, map[string]string{"a": "b"})} {
			if err := Check(name, d, all); err == nil {
				t.Errorf("%s holding %s was accepted", name, what)
			}
		}
	}
}

func TestPictureTricks(t *testing.T) {
	p := pngBytes(t)
	self, _ := os.ReadFile(os.Args[0])
	if len(self) > 4096 {
		self = self[:4096]
	}
	if err := Check("a.png", append(append([]byte{}, p...), self...), all); err == nil {
		t.Error("a program appended to a PNG was accepted")
	}
	if err := Check("a.png", append(append([]byte{}, p...), zipBytes(t, map[string]string{"x": strings.Repeat("y", 100)})...), all); err == nil {
		t.Error("a zip appended to a PNG was accepted")
	}
	if err := Check("a.png", []byte("not a picture at all"), all); err == nil {
		t.Error("text named .png was accepted")
	}
	if err := Check("a.jpg", p, all); err == nil {
		t.Error("a PNG named .jpg was accepted")
	}
}

func TestPDFActiveContentRefused(t *testing.T) {
	bad := map[string][]byte{
		"javascript":   pdf("/OpenAction << /S /JavaScript /JS (app.alert(1)) >>"),
		"launch":       pdf("/OpenAction << /S /Launch /F (cmd.exe) >>"),
		"escaped name": pdf("/OpenAction << /S /J#61vaScript /JS (x) >>"),
		"embedded":     pdf("/Names << /EmbeddedFiles 3 0 R >>"),
		"encrypted":    pdf("/Encrypt 9 0 R"),
		"ascii85":      pdf("/Filter /ASCII85Decode"),
		"filter array": pdf("/Filter [/ASCIIHexDecode /FlateDecode]"),
		"program":      append(pdf(""), []byte("\x7fELF\x02\x01\x01\x00")...),
	}
	// the dangerous dictionary hidden in a compressed object stream
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write([]byte("<< /S /JavaScript /JS (x) >>"))
	_ = zw.Close()
	bad["compressed object stream"] = append(append(pdf("/Filter /FlateDecode"), []byte("5 0 obj\n<< /Type /ObjStm /Filter /FlateDecode >>\nstream\n")...), append(z.Bytes(), []byte("\nendstream\n")...)...)
	// an executable hidden in a compressed stream
	z.Reset()
	zw = zlib.NewWriter(&z)
	self, _ := os.ReadFile(os.Args[0])
	if len(self) > 4096 {
		self = self[:4096]
	}
	_, _ = zw.Write(self)
	_ = zw.Close()
	bad["program in a stream"] = append(append(pdf(""), []byte("6 0 obj\n<< /Filter /FlateDecode >>\nstream\n")...), append(z.Bytes(), []byte("\nendstream\n")...)...)
	for n, d := range bad {
		if err := Check("a.pdf", d, all); err == nil {
			t.Errorf("PDF with %s was accepted", n)
		}
	}
	if err := Check("a.pdf", []byte("hello"), all); err == nil {
		t.Error("text named .pdf was accepted")
	}
	// An ordinary link is fine.
	if err := Check("a.pdf", pdf("/Annots [<< /A << /S /URI /URI (https://example.com) >> >>]"), all); err != nil {
		t.Errorf("a PDF with a web link was refused: %v", err)
	}
}

func TestDocumentMacrosAndEmbeddedRefused(t *testing.T) {
	self, _ := os.ReadFile(os.Args[0])
	cases := map[string][]byte{
		"macros":         docx(t, map[string]string{"word/vbaProject.bin": "x"}),
		"embedded":       docx(t, map[string]string{"word/embeddings/oleObject1.bin": "x"}),
		"activex":        docx(t, map[string]string{"word/activeX/activeX1.xml": "x"}),
		"program":        docx(t, map[string]string{"word/media/image1.png": string(self[:4096])}),
		"nested zip":     docx(t, map[string]string{"word/media/a": string(zipBytes(t, map[string]string{"a": "b"}))}),
		"script":         docx(t, map[string]string{"word/run.js": "x"}),
		"no content map": zipBytes(t, map[string]string{"a.txt": "x"}),
		"unsafe name":    docx(t, map[string]string{"../evil": "x"}),
		"odf basic":      zipBytes(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "META-INF/manifest.xml": "<m/>", "Basic/Standard/Module1.xml": "x"}),
		"zip bomb":       docx(t, map[string]string{"word/big.xml": strings.Repeat("A", 60<<20)}),
	}
	for n, d := range cases {
		name := "a.docx"
		if n == "odf basic" {
			name = "a.odt"
		}
		if err := Check(name, d, all); err == nil {
			t.Errorf("document with %s was accepted", n)
		}
	}
	if err := Check("a.docx", []byte("PK\x03\x04 not really"), all); err == nil {
		t.Error("a damaged document was accepted")
	}
	if err := Check("a.doc", []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), all); err == nil {
		t.Error("an old binary Office file was accepted")
	}
}

func TestEncodedTextRefused(t *testing.T) {
	self, _ := os.ReadFile(os.Args[0])
	if len(self) > 3000 {
		self = self[:3000]
	}
	b64 := base64.StdEncoding.EncodeToString(self)
	cases := map[string]string{
		"base64 on one line": b64,
		"base64 wrapped":     strings.Join(chunks(b64, 76), "\n"),
		"hex wrapped":        strings.Join(chunks(hex.EncodeToString(self), 60), "\n"),
		"hex spaced":         strings.Repeat("de ad be ef ", 40),
		"data uri":           "<img src=\"data:image/png;base64," + b64 + "\">",
		"hex":                strings.Repeat("deadbeef", 40),
		"uuencode":           "begin 644 a.bin\n",
		"script":             "#!/usr/bin/env python3\nprint(1)\n",
		"binary":             "abc\x00def",
	}
	for n, d := range cases {
		if err := Check("a.txt", []byte(d), all); err == nil {
			t.Errorf("%s text was accepted", n)
		}
	}
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func TestKindsAndNames(t *testing.T) {
	if err := Check("a.png", pngBytes(t), map[string]bool{KindText: true}); err == nil {
		t.Error("a kind the admin switched off was accepted")
	}
	for _, n := range []string{".hidden.txt", "a‮txt.exe", "", strings.Repeat("a", 201) + ".txt", "tab\t.txt"} {
		if err := Check(n, []byte("hi"), all); err == nil {
			t.Errorf("name %q was accepted", n)
		}
	}
	if err := Check("a.txt", nil, all); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := ParseKinds("images, pdf, nonsense"); err == nil {
		t.Error("unknown kind accepted")
	}
	if k, err := ParseKinds("images, pdf"); err != nil || !k[KindImages] || !k[KindPDF] || k[KindText] {
		t.Errorf("ParseKinds: %v %v", k, err)
	}
}
