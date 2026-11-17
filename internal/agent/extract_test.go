// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

func TestKindOf(t *testing.T) {
	for name, want := range map[string]Kind{"a.pdf": KindPDF, "A.PDF": KindPDF, "x.PNG": KindImage, "y.jpeg": KindImage, "z.webp": KindImage, "main.go": KindText, "noext": KindText, "pdf": KindText} {
		if got := KindOf(name); got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
	if ImageMIME("a.JPG") != "image/jpeg" || ImageMIME("a.pdf") != "" {
		t.Fatal("ImageMIME")
	}
}

func TestReadFileUsesExtractionForPDFsAndPictures(t *testing.T) {
	x, b := newExec()
	var got []string
	b.run = func(argv []string) (sandbox.ExecResult, bool) {
		if len(argv) > 4 && strings.Contains(argv[2], "pdftotext") {
			got = argv
			return sandbox.ExecResult{Stdout: "[OCR]\nINVOICE 1\n"}, true
		}
		return sandbox.ExecResult{}, false
	}
	o := run(x, ToolRead, `{"path":".attachments/scan.pdf"}`)
	if o.IsError || !strings.Contains(o.Text, "INVOICE 1") || !o.Untrusted {
		t.Fatalf("%+v", o)
	}
	if len(got) < 5 || got[3] != "_" || got[4] != "/workspace/.attachments/scan.pdf" || got[5] != "pdf" {
		t.Fatalf("path and kind must be arguments, not part of the script: %v", got)
	}
	o = run(x, ToolRead, `{"path":"shot.png"}`)
	if o.IsError || !o.Untrusted || got[5] != "image" || got[4] != "/workspace/shot.png" {
		t.Fatalf("%+v", o)
	}
	if last := b.cmds[len(b.cmds)-1]; !strings.Contains(last, "tesseract") {
		t.Fatalf("a picture was not sent to OCR: %s", last)
	}
	if o := run(x, ToolRead, `{"path":"/etc/shadow.pdf"}`); !o.IsError || !strings.Contains(o.Text, "outside the workspace") {
		t.Fatalf("a PDF outside the workspace was read: %+v", o)
	}
}

func TestExtractReportsMissingOCRPlainly(t *testing.T) {
	_, b := newExec()
	b.run = func([]string) (sandbox.ExecResult, bool) {
		return sandbox.ExecResult{ExitCode: 6, Stderr: "OCR (tesseract) is not installed in this sandbox image\n"}, true
	}
	_, err := ExtractText(context.Background(), b.exec, "/workspace/a.png", KindImage)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatal(err)
	}
	if _, err := ExtractText(context.Background(), b.exec, "/workspace/a.txt", KindText); err == nil {
		t.Fatal("a text file went to extraction")
	}
}
