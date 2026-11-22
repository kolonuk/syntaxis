// SPDX-License-Identifier: GPL-2.0-or-later

// Package uploadcheck decides whether an uploaded file may be stored in a workspace. It is an
// allow-list: a file is accepted only when its name says it is a picture, a PDF, an office document
// or plain text, AND its content really is that and carries nothing that runs or hides something
// that runs (executables, scripts, archives, macros, embedded files, encoded blobs). Anything it
// cannot inspect is refused. It looks at bytes only and never runs or opens the file.
package uploadcheck

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Kinds an admin can allow.
const (
	KindImages    = "images"
	KindPDF       = "pdf"
	KindDocuments = "documents"
	KindText      = "text"
)

// AllKinds lists every kind, in the order shown to the admin.
var AllKinds = []string{KindImages, KindPDF, KindDocuments, KindText}

// byExt maps a lower-case extension to its kind.
var byExt = map[string]string{
	".png": KindImages, ".jpg": KindImages, ".jpeg": KindImages, ".gif": KindImages, ".webp": KindImages, ".bmp": KindImages, ".tif": KindImages, ".tiff": KindImages,
	".pdf":  KindPDF,
	".docx": KindDocuments, ".xlsx": KindDocuments, ".pptx": KindDocuments, ".odt": KindDocuments, ".ods": KindDocuments, ".odp": KindDocuments,
	".txt": KindText, ".md": KindText, ".csv": KindText, ".tsv": KindText, ".json": KindText, ".log": KindText,
}

// ParseKinds turns an admin's comma list into the allowed kinds; unknown words are an error.
func ParseKinds(s string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(strings.ToLower(k))
		if k == "" {
			continue
		}
		ok := false
		for _, a := range AllKinds {
			ok = ok || a == k
		}
		if !ok {
			return nil, fmt.Errorf("unknown kind %q (use %s)", k, strings.Join(AllKinds, ", "))
		}
		out[k] = true
	}
	return out, nil
}

// Limits for what the checker is willing to inspect.
const (
	maxZipEntries  = 2000
	maxZipExpanded = 200 << 20 // all entries together, uncompressed
	maxZipRatio    = 200       // total uncompressed / compressed size
	maxPDFInflate  = 64 << 20  // all decoded PDF streams together
	minEncodedRun  = 200       // characters in a row that look like base64 or hex
)

// Check returns nil when the file may be stored. allowed is the set of kinds the admin turned on.
func Check(name string, data []byte, allowed map[string]bool) error {
	if len(data) == 0 {
		return errors.New("the file is empty")
	}
	if err := checkName(name); err != nil {
		return err
	}
	ext := strings.ToLower(path.Ext(name))
	kind, known := byExt[ext]
	if !known {
		return fmt.Errorf("%s are not accepted; only pictures, PDFs, office documents and plain text are", orNone(ext))
	}
	if !allowed[kind] {
		return fmt.Errorf("%s files are not accepted on this server", kind)
	}
	// Whatever the name says, executables and archives never pass.
	if why := dangerousMagic(data); why != "" {
		return fmt.Errorf("the content is %s, which is not accepted", why)
	}
	if kind != KindDocuments && bytes.HasPrefix(data, []byte("PK")) {
		return errors.New("the content is an archive, which is not accepted")
	}
	switch kind {
	case KindImages:
		return checkImage(ext, data)
	case KindPDF:
		return checkPDF(data)
	case KindDocuments:
		return checkDocument(ext, data)
	default:
		return checkText(ext, data)
	}
}

func orNone(ext string) string {
	if ext == "" {
		return "Files without an extension"
	}
	return ext + " files"
}

func checkName(name string) error {
	if name == "" || len(name) > 200 {
		return errors.New("the file name is empty or too long")
	}
	if strings.HasPrefix(name, ".") {
		return errors.New("file names cannot start with a dot")
	}
	for _, r := range name {
		// control characters and the right-to-left override used to fake an extension
		if r < 32 || r == 127 || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff {
			return errors.New("the file name has hidden or control characters")
		}
	}
	return nil
}

// dangerousMagic names what the first bytes say the file is, when that is something never accepted.
func dangerousMagic(b []byte) string {
	has := func(p string) bool { return bytes.HasPrefix(b, []byte(p)) }
	switch {
	case has("\x7fELF"):
		return "a Linux program"
	case has("MZ") && isPE(b):
		return "a Windows program"
	case has("\xfe\xed\xfa\xce"), has("\xfe\xed\xfa\xcf"), has("\xce\xfa\xed\xfe"), has("\xcf\xfa\xed\xfe"), has("\xca\xfe\xba\xbe"), has("\xbe\xba\xfe\xca"):
		return "a macOS program or Java class"
	case has("#!"):
		return "a script"
	case has("PK\x03\x04"), has("PK\x05\x06"), has("PK\x07\x08"):
		return "" // a zip container: only the document check decides (below), other kinds fail their own check
	case has("\x1f\x8b"):
		return "a gzip archive"
	case has("BZh"):
		return "a bzip2 archive"
	case has("\xfd7zXZ\x00"):
		return "an xz archive"
	case has("7z\xbc\xaf\x27\x1c"):
		return "a 7-zip archive"
	case has("Rar!\x1a\x07"):
		return "a RAR archive"
	case has("\x28\xb5\x2f\xfd"):
		return "a zstd archive"
	case has("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"):
		return "an old Office or installer file (it can carry macros)"
	case has("MSCF"):
		return "a cabinet archive"
	case len(b) > 262 && string(b[257:262]) == "ustar":
		return "a tar archive"
	case has("\xed\xab\xee\xdb"), has("!<arch>\n"), has("\x00asm"):
		return "a package or binary"
	}
	return ""
}

func isPE(b []byte) bool {
	if len(b) < 0x40 {
		return false
	}
	off := int(binary.LittleEndian.Uint32(b[0x3c:0x40]))
	return off > 0 && off+4 <= len(b) && string(b[off:off+4]) == "PE\x00\x00"
}

// embeddedProgram reports a program hidden inside other data.
func embeddedProgram(b []byte) string {
	if i := bytes.Index(b, []byte("This program cannot be run in DOS mode")); i >= 0 {
		return "a Windows program"
	}
	// ELF header with 32/64-bit class, little/big endian and version 1.
	for i := 0; ; {
		j := bytes.Index(b[i:], []byte("\x7fELF"))
		if j < 0 {
			break
		}
		j += i
		if j+7 <= len(b) && (b[j+4] == 1 || b[j+4] == 2) && (b[j+5] == 1 || b[j+5] == 2) && b[j+6] == 1 {
			return "a Linux program"
		}
		i = j + 4
	}
	return ""
}

// ---- pictures ----

func checkImage(ext string, b []byte) error {
	var ok bool
	var end []byte // marker that ends the picture; nothing may follow it
	switch ext {
	case ".png":
		ok = bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n"))
		end = []byte("IEND\xaeB`\x82")
	case ".jpg", ".jpeg":
		ok = bytes.HasPrefix(b, []byte("\xff\xd8\xff"))
		end = []byte("\xff\xd9")
	case ".gif":
		ok = bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
		end = []byte(";")
	case ".webp":
		ok = len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
		if ok && int(binary.LittleEndian.Uint32(b[4:8]))+8 != len(b) {
			return errors.New("the picture has extra data after its end")
		}
	case ".bmp":
		ok = bytes.HasPrefix(b, []byte("BM"))
	case ".tif", ".tiff":
		ok = bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*"))
	}
	if !ok {
		return fmt.Errorf("the content is not a %s picture", strings.TrimPrefix(ext, "."))
	}
	if end != nil {
		i := bytes.LastIndex(b, end)
		if i < 0 {
			return errors.New("the picture is cut short or damaged")
		}
		// A little padding after the end is tolerated; a whole file hidden after it is not.
		if len(b)-(i+len(end)) > 16 {
			return errors.New("the picture has extra data after its end")
		}
	}
	if why := embeddedProgram(b); why != "" {
		return fmt.Errorf("the picture hides %s", why)
	}
	if bytes.Contains(b, []byte("PK\x03\x04")) {
		return errors.New("the picture hides an archive")
	}
	return nil
}

// ---- PDF ----

// Names that make a PDF do something or carry another file. They are looked for in the file and in
// its decoded streams (where object streams hide them), after undoing #xx escapes in names.
var pdfActive = []string{
	"/JavaScript", "/JS", "/Launch", "/EmbeddedFile", "/EmbeddedFiles", "/OpenAction", "/AA", "/RichMedia", "/XFA",
	"/SubmitForm", "/ImportData", "/GoToE", "/Movie", "/Sound", "/Rendition", "/3D", "/FileAttachment",
	"/Encrypt", "/Crypt",
}

// Stream filters that only wrap pictures; any other filter could hide content from this check.
var pdfFilterOK = map[string]bool{"FlateDecode": true, "Fl": true, "DCTDecode": true, "DCT": true, "JPXDecode": true, "CCITTFaxDecode": true, "CCF": true, "JBIG2Decode": true}

var (
	pdfStreamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	pdfFilterRe = regexp.MustCompile(`/Filter\s*(\[[^\]]*\]|/[A-Za-z0-9#]+)`)
	pdfNameRe   = regexp.MustCompile(`/[A-Za-z0-9#]+`)
	pdfHexEsc   = regexp.MustCompile(`#([0-9A-Fa-f]{2})`)
)

func checkPDF(b []byte) error {
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	if !bytes.Contains(head, []byte("%PDF-")) {
		return errors.New("the content is not a PDF")
	}
	if why := embeddedProgram(b); why != "" {
		return fmt.Errorf("the PDF hides %s", why)
	}
	if err := scanPDF(b); err != nil {
		return err
	}
	// Decode streams (object streams hide dictionaries) and scan what comes out.
	budget := maxPDFInflate
	for _, m := range pdfStreamRe.FindAllSubmatch(b, -1) {
		raw := m[1]
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			continue // a picture stream or not zlib; the filter list above decides what is allowed
		}
		out, err := io.ReadAll(io.LimitReader(zr, int64(budget)+1))
		if len(out) > budget {
			return errors.New("the PDF expands to too much data to check")
		}
		budget -= len(out)
		if err != nil && len(out) == 0 {
			continue
		}
		if why := embeddedProgram(out); why != "" {
			return fmt.Errorf("the PDF hides %s", why)
		}
		if bytes.HasPrefix(out, []byte("PK\x03\x04")) {
			return errors.New("the PDF hides an archive")
		}
		if err := scanPDF(out); err != nil {
			return err
		}
	}
	return nil
}

func scanPDF(b []byte) error {
	// Undo name escapes (/J#61vaScript) and look for the dangerous names as whole names.
	for _, n := range pdfNameRe.FindAll(b, -1) {
		name := pdfHexEsc.ReplaceAllFunc(n, func(h []byte) []byte {
			var v byte
			_, _ = fmt.Sscanf(string(h[1:]), "%02x", &v)
			return []byte{v}
		})
		for _, bad := range pdfActive {
			if string(name) == bad {
				return fmt.Errorf("the PDF contains %s (scripts, launch actions, forms, links to open or attached files are not accepted)", bad)
			}
		}
	}
	for _, f := range pdfFilterRe.FindAllSubmatch(b, -1) {
		for _, n := range pdfNameRe.FindAll(f[1], -1) {
			name := strings.TrimPrefix(string(pdfHexEsc.ReplaceAllFunc(n, func(h []byte) []byte {
				var v byte
				_, _ = fmt.Sscanf(string(h[1:]), "%02x", &v)
				return []byte{v}
			})), "/")
			if name != "Filter" && !pdfFilterOK[name] {
				return fmt.Errorf("the PDF uses the %s encoding, which cannot be checked", name)
			}
		}
	}
	return nil
}

// ---- office documents ----

func checkDocument(ext string, b []byte) error {
	if !bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		return errors.New("the content is not an office document (old binary formats are not accepted)")
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return errors.New("the document is damaged")
	}
	if len(zr.File) == 0 || len(zr.File) > maxZipEntries {
		return errors.New("the document has no parts or too many")
	}
	var total uint64
	names := map[string]bool{}
	for _, f := range zr.File {
		n := strings.ToLower(f.Name)
		names[n] = true
		if f.Flags&1 != 0 {
			return errors.New("the document is encrypted, so it cannot be checked")
		}
		if strings.HasPrefix(n, "/") || strings.Contains(n, "..") || strings.Contains(n, "\\") {
			return errors.New("the document has an unsafe part name")
		}
		total += f.UncompressedSize64
		if total > maxZipExpanded {
			return errors.New("the document expands to too much data")
		}
		if bad := officeBadPart(n); bad != "" {
			return fmt.Errorf("the document contains %s", bad)
		}
	}
	if total/uint64(len(b)+1) > maxZipRatio {
		return errors.New("the document is compressed suspiciously well")
	}
	switch ext {
	case ".docx", ".xlsx", ".pptx":
		if !names["[content_types].xml"] {
			return errors.New("the content is not an Office Open XML document")
		}
	default:
		if !names["mimetype"] || !names["meta-inf/manifest.xml"] {
			return errors.New("the content is not an OpenDocument file")
		}
	}
	for _, f := range zr.File {
		if f.UncompressedSize64 > 64<<20 {
			return errors.New("a part of the document is too large to check")
		}
		rc, err := f.Open()
		if err != nil {
			return errors.New("the document is damaged")
		}
		head := make([]byte, 4096)
		n, _ := io.ReadFull(rc, head)
		rest, _ := io.ReadAll(io.LimitReader(rc, 64<<20))
		_ = rc.Close()
		body := append(head[:n], rest...)
		if why := dangerousMagic(body); why != "" && !bytes.HasPrefix(body, []byte("PK")) {
			return fmt.Errorf("the document contains %s", why)
		}
		if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
			return errors.New("the document contains another archive")
		}
		if why := embeddedProgram(body); why != "" {
			return fmt.Errorf("the document hides %s", why)
		}
	}
	return nil
}

// officeBadPart names a part that carries macros, scripts or embedded objects.
func officeBadPart(n string) string {
	switch {
	case strings.Contains(n, "vbaproject"), strings.Contains(n, "vbadata"):
		return "macros"
	case strings.Contains(n, "/embeddings/"), strings.HasPrefix(n, "embeddings/"), strings.Contains(n, "/activex/"), strings.Contains(n, "oleobject"):
		return "embedded objects"
	case strings.HasPrefix(n, "basic/"), strings.HasPrefix(n, "scripts/"), strings.HasPrefix(n, "macros/"), strings.Contains(n, "/macros/"):
		return "macros"
	case strings.HasPrefix(n, "object ") || strings.Contains(n, "/object "):
		return "embedded objects"
	}
	for _, s := range []string{".exe", ".dll", ".bat", ".cmd", ".com", ".js", ".vbs", ".ps1", ".sh", ".jar", ".msi", ".scr", ".lnk", ".hta", ".so", ".bin"} {
		if strings.HasSuffix(n, s) {
			return "a " + s + " file"
		}
	}
	return ""
}

// ---- text ----

var (
	encodedRun = regexp.MustCompile(fmt.Sprintf(`[A-Za-z0-9+/_-]{%d,}={0,2}`, minEncodedRun))
	hexInline  = regexp.MustCompile(`[0-9A-Fa-f]{128,}`)
	hexSpaced  = regexp.MustCompile(`(?:\b[0-9A-Fa-f]{2}\b[ :]){64,}`)
	b64Line    = regexp.MustCompile(`^[A-Za-z0-9+/_-]{40,}={0,2}$`)
	hexLine    = regexp.MustCompile(`^[0-9A-Fa-f]{32,}$`)
)

const (
	wrappedBase64Chars = 400  // base64 wrapped over several lines
	wrappedHexChars    = 1000 // hexadecimal wrapped over many lines (a list of commit hashes is shorter)
)

func mixedCase(s string) bool {
	return strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") && strings.ContainsAny(s, "0123456789")
}

func checkText(ext string, b []byte) error {
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return errors.New("the content is not plain text")
	}
	s := string(b)
	if strings.HasPrefix(strings.TrimLeft(s, "\ufeff \t\r\n"), "#!") {
		return errors.New("scripts are not accepted")
	}
	for _, run := range encodedRun.FindAllString(s, -1) {
		// a long run with capitals, small letters and digits is data, not a separator or a word
		if mixedCase(run) {
			return errors.New("the text holds a long run of base64 or similar encoded data, which is not accepted")
		}
	}
	for _, run := range hexInline.FindAllString(s, -1) {
		if strings.ContainsAny(run, "abcdefABCDEF") {
			return errors.New("the text holds a long run of hexadecimal data, which is not accepted")
		}
	}
	for _, run := range hexSpaced.FindAllString(s, -1) {
		if strings.ContainsAny(run, "abcdefABCDEF") {
			return errors.New("the text holds a hexadecimal dump, which is not accepted")
		}
	}
	// the same data wrapped over many short lines, as base64 and xxd -p tools do
	b64, hex := 0, 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case b64Line.MatchString(line) && mixedCase(line):
			b64 += len(line)
		case hexLine.MatchString(line) && strings.ContainsAny(line, "abcdefABCDEF"):
			hex += len(line)
		default:
			continue
		}
		if b64 >= wrappedBase64Chars {
			return errors.New("the text holds base64 data wrapped over several lines, which is not accepted")
		}
		if hex >= wrappedHexChars {
			return errors.New("the text holds hexadecimal data wrapped over many lines, which is not accepted")
		}
	}
	if strings.Contains(s, "begin 644 ") || strings.Contains(s, "begin 755 ") || strings.Contains(s, "begin-base64") {
		return errors.New("the text holds an encoded file")
	}
	return nil
}
