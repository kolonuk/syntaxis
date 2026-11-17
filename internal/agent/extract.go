// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// Documents and pictures are read inside the sandbox: PDFs with poppler (the text layer first, and
// OCR of the pages when there is none, as in a scan) and pictures with tesseract. Parsing files of
// unknown origin is exactly what the sandbox is for, and the result is untrusted data.

// Kind says how a file is read.
type Kind string

// Kinds of attached or read files.
const (
	KindText  Kind = "text"
	KindPDF   Kind = "pdf"
	KindImage Kind = "image"
)

var imageExt = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp", ".tif": "image/tiff", ".tiff": "image/tiff"}

// KindOf classifies a file by its name.
func KindOf(name string) Kind {
	ext := strings.ToLower(path.Ext(name))
	if ext == ".pdf" {
		return KindPDF
	}
	if _, ok := imageExt[ext]; ok {
		return KindImage
	}
	return KindText
}

// ImageMIME is the type of a picture file, or "" if the name is not a picture.
func ImageMIME(name string) string { return imageExt[strings.ToLower(path.Ext(name))] }

// extractScript prints the text of a PDF or picture ($1 = file, $2 = pdf or image). The first line
// says where the text came from. Exit codes: 5 = poppler missing, 6 = tesseract missing.
const extractScript = `
f="$1"
if [ "$2" = pdf ]; then
  command -v pdftotext >/dev/null 2>&1 || { echo "pdftotext is not installed in this sandbox image" >&2; exit 5; }
  t=$(pdftotext -layout -l 100 -- "$f" - 2>/dev/null)
  if [ "$(printf %s "$t" | tr -d '[:space:]' | wc -c)" -ge 20 ]; then echo "[text layer]"; printf '%s\n' "$t"; exit 0; fi
  command -v tesseract >/dev/null 2>&1 && command -v pdftoppm >/dev/null 2>&1 || { echo "this PDF has no text layer and OCR (tesseract, pdftoppm) is not installed in this sandbox image" >&2; exit 6; }
  d=$(mktemp -d) || exit 1
  pdftoppm -r 200 -l 30 -png -- "$f" "$d/p" 2>/dev/null || { rm -rf "$d"; echo "the PDF could not be read" >&2; exit 1; }
  echo "[OCR]"
  n=0
  for p in "$d"/p*.png; do n=$((n+1)); echo "--- page $n ---"; tesseract "$p" stdout 2>/dev/null; done
  rm -rf "$d"
else
  command -v tesseract >/dev/null 2>&1 || { echo "OCR (tesseract) is not installed in this sandbox image" >&2; exit 6; }
  echo "[OCR]"
  tesseract "$f" stdout 2>/dev/null || { echo "the picture could not be read" >&2; exit 1; }
fi
`

// ExtractText returns the text of a PDF or picture in the workspace.
func ExtractText(ctx context.Context, exec func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error), p string, kind Kind) (string, error) {
	if kind != KindPDF && kind != KindImage {
		return "", fmt.Errorf("%s is not a PDF or a picture", p)
	}
	r, err := exec(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", extractScript, "_", p, string(kind)}, Timeout: 4 * time.Minute, MaxOutput: 400 << 10})
	if err != nil {
		return "", err
	}
	switch r.ExitCode {
	case 0:
		return strings.TrimSpace(r.Stdout), nil
	default:
		msg := strings.TrimSpace(r.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", r.ExitCode)
		}
		return "", fmt.Errorf("cannot read %s: %s", p, msg)
	}
}
