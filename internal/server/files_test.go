// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

func (c *client) doRaw(method, path, contentType string, body []byte) (int, map[string]any, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func TestParseListingSortsFoldersFirst(t *testing.T) {
	out := "f\t12\t1700000000.5\tb.txt\nd\t4096\t1700000001.0\tsrc\nf\t3\t1700000002.0\tA.md\nl\t9\t1700000003.0\tlink\nbad line\n"
	es := parseListing(out)
	var names []string
	for _, e := range es {
		names = append(names, e.Name+":"+e.Type)
	}
	if got := strings.Join(names, " "); got != "src:dir A.md:file b.txt:file link:link" {
		t.Fatal(got)
	}
	if es[1].Size != 3 || es[1].Modified.Unix() != 1700000002 {
		t.Fatalf("%+v", es[1])
	}
}

func TestCleanUploadName(t *testing.T) {
	good := map[string]string{"a.txt": "a.txt", "C:\\fake\\report.pdf": "report.pdf", "../../etc/passwd": "passwd", "dir/sub/x.png": "x.png", " spaced name.md ": "spaced name.md", "tab\x00in.txt": "tabin.txt"}
	for in, want := range good {
		if got, ok := cleanUploadName(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "/", "   ", strings.Repeat("x", 300)} {
		if _, ok := cleanUploadName(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestIsText(t *testing.T) {
	if !isText([]byte("héllo\nworld")) || !isText(nil) {
		t.Fatal("text")
	}
	if isText([]byte("a\x00b")) || isText([]byte{0xff, 0xfe, 0xfd}) {
		t.Fatal("binary taken for text")
	}
}

func TestFilesNeedSignInAndOwnership(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	owner := e.userSession("fo@example.com")
	other := e.userSession("fx@example.com")
	_, s, _ := owner.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	anon := e.client(e.user.URL)
	for _, p := range []string{"/files", "/files/content?path=a", "/files/download?path=a"} {
		if code, _, _ := anon.do("GET", "/api/sessions/"+id+p, nil); code != 401 {
			t.Errorf("anonymous %s: %d", p, code)
		}
		if code, _, _ := other.do("GET", "/api/sessions/"+id+p, nil); code != 404 {
			t.Errorf("another user %s: %d", p, code)
		}
	}
	if code, _, _ := other.doRaw("POST", "/api/sessions/"+id+"/files/upload?name=x", "application/octet-stream", []byte("x")); code != 404 {
		t.Errorf("another user uploaded: %d", code)
	}
	// nothing can be written before the sandbox exists (an upload creates it)
	if code, _, _ := owner.do("PUT", "/api/sessions/"+id+"/files/content", map[string]string{"path": "/workspace/a.txt", "text": "x"}); code != 409 {
		t.Errorf("edit with no sandbox: %d", code)
	}
	if code, _, _ := owner.do("DELETE", "/api/sessions/"+id+"/files?path=/workspace/a.txt", nil); code != 409 {
		t.Errorf("delete with no sandbox: %d", code)
	}
	// a session that has not run has no files to show
	code, body, _ := owner.do("GET", "/api/sessions/"+id+"/files", nil)
	if code != 200 || body["available"] != false {
		t.Fatalf("%d %v", code, body)
	}
}

// The file browser against a real gVisor sandbox.
func TestIntegrationFileBrowser(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	u := e.userSession("fb@example.com")
	uid := userID(t, e, "fb@example.com")
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

	// an upload starts the sandbox
	code, up, raw := u.doRaw("POST", base+"/files/upload?dir=/workspace/docs&name="+url.QueryEscape("../notes.txt"), "application/octet-stream", []byte("hello\nworld\n"))
	if code != 200 || up["path"] != "/workspace/docs/notes.txt" {
		t.Fatalf("%d %s", code, raw)
	}
	bin := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 255, 254, 0}
	if code, _, raw := u.doRaw("POST", base+"/files/upload?name=pic.png", "application/octet-stream", bin); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}

	// list: folders first, sizes right
	_, ls, _ := u.do("GET", base+"/files", nil)
	entries := ls["entries"].([]any)
	if ls["available"] != true || len(entries) != 2 || entries[0].(map[string]any)["name"] != "docs" || entries[1].(map[string]any)["size"] != float64(len(bin)) {
		t.Fatalf("%v", ls)
	}
	// the attachments folder and the shadow repository are not clutter that a listing must hide
	if code, _, _ := u.do("GET", base+"/files?path=/workspace/nope", nil); code != 404 {
		t.Fatalf("missing folder: %d", code)
	}

	// view as text, and a binary file is not shown as text
	_, v, _ := u.do("GET", base+"/files/content?path=/workspace/docs/notes.txt", nil)
	if v["text"] != "hello\nworld\n" || v["binary"] != false {
		t.Fatalf("%v", v)
	}
	_, v, _ = u.do("GET", base+"/files/content?path=/workspace/pic.png", nil)
	if v["binary"] != true || v["text"] != nil {
		t.Fatalf("%v", v)
	}

	// edit, then see the change; the file on disk is what the editor sent
	if code, _, raw := u.do("PUT", base+"/files/content", map[string]string{"path": "/workspace/docs/notes.txt", "text": "changed\n"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, v, _ = u.do("GET", base+"/files/content?path=docs/notes.txt", nil)
	if v["text"] != "changed\n" {
		t.Fatalf("%v", v)
	}

	// download returns the exact bytes, and forces a download
	req, _ := http.NewRequest("GET", u.base+base+"/files/download?path=/workspace/pic.png", nil)
	resp, err := u.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, bin) || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %v %q", resp.Header, got)
	}

	// paths outside the workspace are refused, for every operation
	for _, p := range []string{"/etc/passwd", "../../etc/passwd", "/workspace/../etc/passwd"} {
		q := "?path=" + url.QueryEscape(p)
		if code, _, _ := u.do("GET", base+"/files"+q, nil); code != 400 {
			t.Errorf("list %s: %d", p, code)
		}
		if code, _, _ := u.do("GET", base+"/files/content"+q, nil); code != 400 {
			t.Errorf("view %s: %d", p, code)
		}
		if code, _, _ := u.do("GET", base+"/files/download"+q, nil); code != 400 {
			t.Errorf("download %s: %d", p, code)
		}
		if code, _, _ := u.do("PUT", base+"/files/content", map[string]string{"path": p, "text": "x"}); code != 400 {
			t.Errorf("edit %s: %d", p, code)
		}
		if code, _, _ := u.do("DELETE", base+"/files"+q, nil); code != 400 {
			t.Errorf("delete %s: %d", p, code)
		}
	}
	if code, _, _ := u.doRaw("POST", base+"/files/upload?dir=/etc&name=x", "application/octet-stream", []byte("x")); code != 400 {
		t.Errorf("upload outside the workspace: %d", code)
	}
	if code, _, _ := u.do("DELETE", base+"/files?path=/workspace", nil); code != 400 {
		t.Errorf("the workspace itself was deletable: %d", code)
	}

	// while the agent is working, nothing can be changed (reading still works)
	release := make(chan struct{})
	if _, started := e.app.Runs.Start(id, func(c context.Context, run *Run) { <-release }); !started {
		t.Fatal("could not start the stand-in run")
	}
	time.Sleep(50 * time.Millisecond)
	if code, _, _ := u.do("PUT", base+"/files/content", map[string]string{"path": "/workspace/docs/notes.txt", "text": "x"}); code != 409 {
		t.Errorf("edit during a run: %d", code)
	}
	if code, _, _ := u.doRaw("POST", base+"/files/upload?name=late.txt", "application/octet-stream", []byte("x")); code != 409 {
		t.Errorf("upload during a run: %d", code)
	}
	if code, _, _ := u.do("DELETE", base+"/files?path=/workspace/pic.png", nil); code != 409 {
		t.Errorf("delete during a run: %d", code)
	}
	if code, _, _ := u.do("GET", base+"/files/content?path=/workspace/docs/notes.txt", nil); code != 200 {
		t.Errorf("reading during a run: %d", code)
	}
	close(release)
	time.Sleep(100 * time.Millisecond)

	// delete a file and a folder
	if code, _, _ := u.do("DELETE", base+"/files?path=/workspace/pic.png", nil); code != 200 {
		t.Fatal("delete file")
	}
	if code, _, _ := u.do("DELETE", base+"/files?path=/workspace/docs", nil); code != 200 {
		t.Fatal("delete folder")
	}
	_, ls, _ = u.do("GET", base+"/files", nil)
	if n := len(ls["entries"].([]any)); n != 0 {
		t.Fatalf("not deleted: %v", ls)
	}
}
