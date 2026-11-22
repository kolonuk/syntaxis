// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func TestSendAttachmentsAreChecked(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	var got msgLog
	e.app.RunAgent = func(_ context.Context, _ *Run, _ *store.ChatSession, _ *store.User, msg string) {
		got.add(msg)
	}
	u := e.userSession("at@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	base := "/api/sessions/" + s["id"].(string)
	// nothing uploaded yet
	if code, b, _ := u.do("POST", base+"/messages", map[string]any{"text": "see", "attachments": []string{agent.AttachmentDir + "/a.png"}}); code != 400 || !strings.Contains(b["error"].(string), "upload") {
		t.Fatalf("%d %v", code, b)
	}
	// too many
	many := make([]string, maxAttachments+1)
	for i := range many {
		many[i] = agent.AttachmentDir + "/x.txt"
	}
	if code, _, _ := u.do("POST", base+"/messages", map[string]any{"text": "see", "attachments": many}); code != 400 {
		t.Fatalf("%d", code)
	}
	// a plain message still works and carries no list
	if code, _, _ := u.do("POST", base+"/messages", map[string]any{"text": "hello"}); code != 202 {
		t.Fatalf("%d", code)
	}
	if got.n() != 1 || strings.Contains(got.at(0), "<attachments>") {
		t.Fatalf("%q", got.all())
	}
}

// PDFs and pictures, uploaded through the file API into a real gVisor sandbox, are read as text by
// the agent's own read_file tool: the text layer of a PDF, and OCR for a scan and for a picture.
func TestIntegrationAttachmentsAreReadWithOCR(t *testing.T) {
	e := newEnv(t)
	_ = e.st.SetSetting(context.Background(), SettingUploadsEnabled, "true")
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	var sent msgLog
	e.app.RunAgent = func(_ context.Context, run *Run, _ *store.ChatSession, _ *store.User, msg string) {
		sent.add(msg)
	}
	u := e.userSession("ocr@example.com")
	uid := userID(t, e, "ocr@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	base := "/api/sessions/" + id
	ctx := context.Background()
	t.Cleanup(func() {
		if cur, err := e.st.GetChatSession(ctx, id); err == nil && cur.SandboxID != "" {
			_ = e.app.Sandbox.Destroy(ctx, id, cur.SandboxID)
		}
		_ = e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid))
	})
	var paths []string
	pngBytes, _ := os.ReadFile("../agent/testdata/ocr.png")
	for _, n := range []string{"ocr.png", "scanned.pdf", "text.pdf"} {
		b, err := os.ReadFile("../agent/testdata/" + n)
		if err != nil {
			t.Fatal(err)
		}
		code, up, raw := u.doRaw("POST", base+"/files/upload?dir="+agent.AttachmentDir+"&name="+n, "application/octet-stream", b)
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		paths = append(paths, up["path"].(string))
	}
	// sending with the paths adds the list to the message
	if code, _, raw := u.do("POST", base+"/messages", map[string]any{"text": "summarise these", "attachments": paths}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	waitFor(t, func() bool { return sent.n() == 1 })
	text, items := agent.ParseAttachments(sent.at(0))
	if text != "summarise these" || len(items) != 3 || items[0].Kind != agent.KindImage || items[1].Kind != agent.KindPDF || items[0].Size != int64(len(pngBytes)) {
		t.Fatalf("%q %+v", text, items)
	}
	// a path outside the attachment folder is refused even though the file exists
	if code, _, _ := u.doRaw("POST", base+"/files/upload?name=other.txt", "application/octet-stream", []byte("x")); code != 200 {
		t.Fatal("upload to the workspace")
	}
	if code, b, _ := u.do("POST", base+"/messages", map[string]any{"text": "x", "attachments": []string{"/workspace/other.txt"}}); code != 400 {
		t.Fatalf("%d %v", code, b)
	}

	cur, _ := e.st.GetChatSession(ctx, id)
	exec := func(c context.Context, spec sandbox.ExecSpec) (sandbox.ExecResult, error) {
		return e.app.Sandbox.Exec(c, id, cur.SandboxID, spec)
	}
	x := &agent.Executor{Exec: exec}
	read := func(p string) agent.Output {
		return x.Run(ctx, agent.ToolRead, []byte(`{"path":"`+p+`"}`))
	}
	notInstalled := func(o agent.Output) {
		if o.IsError && strings.Contains(o.Text, "not installed") {
			t.Skip("NOT TESTED: the sandbox image has no OCR tools; rebuild it (docker build -t syntaxis-sandbox:latest images/sandbox) or set SYNTAXIS_TEST_SANDBOX_IMAGE")
		}
	}
	o := read(paths[0])
	notInstalled(o)
	if o.IsError || !o.Untrusted || !strings.Contains(o.Text, "[OCR]") || !strings.Contains(o.Text, "INVOICE 4721") || !strings.Contains(o.Text, "305") {
		t.Fatalf("picture: %+v", o)
	}
	o = read(paths[1])
	if o.IsError || !strings.Contains(o.Text, "[OCR]") || !strings.Contains(o.Text, "--- page 1 ---") || !strings.Contains(o.Text, "INVOICE 4721") {
		t.Fatalf("scanned PDF: %+v", o)
	}
	o = read(paths[2])
	if o.IsError || !strings.Contains(o.Text, "[text layer]") || !strings.Contains(o.Text, "Quarterly report 9182") || strings.Contains(o.Text, "[OCR]") {
		t.Fatalf("PDF with text: %+v", o)
	}
	// a file that is not a real PDF is reported, not mistaken for an empty document
	if code, _, _ := u.doRaw("POST", base+"/files/upload?name=fake.pdf", "application/octet-stream", []byte("this is not a pdf")); code != 415 {
		t.Fatalf("a fake PDF was accepted by upload: %d", code)
	}
	if err := e.app.writeFileBytes(ctx, cur, cur.SandboxID, "/workspace/fake.pdf", []byte("this is not a pdf")); err != nil {
		t.Fatal(err)
	}
	if o := read("fake.pdf"); !o.IsError && !strings.Contains(o.Text, "no text found") {
		t.Fatalf("%+v", o)
	}

	// the picture reaches a vision model as bytes
	imgs := agent.LoadImages(ctx, exec, sent.at(0))
	if len(imgs) != 1 || !bytes.Equal(imgs[0].Data, pngBytes) || imgs[0].MIME != "image/png" {
		t.Fatalf("%d images", len(imgs))
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}
