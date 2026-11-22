// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/uploadcheck"
)

// The file browser works on the session's own workspace, through the session's sandbox: nothing is
// read or written on the host. Paths are resolved inside /workspace (agent.SafePath) like the
// agent's own file tools.
const (
	maxViewBytes     = 1 << 20  // largest file shown or edited as text
	maxUploadMBLimit = 100      // the most an admin can allow for one upload, in MiB
	maxDownload      = 25 << 20 // largest download
	maxListEntries   = 2000
)

// FileEntry is one item of a directory listing.
type FileEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"` // dir | file | link | other
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// sessionSandbox returns the session's sandbox, starting one when create is set.
func (a *App) sessionSandbox(ctx context.Context, s *store.ChatSession, u *store.User, create bool) (string, error) {
	if s.SandboxID == "" && !create {
		return "", errNoSandbox
	}
	if a.Sandbox == nil {
		return "", errors.New("sandboxes are not available on this server")
	}
	return a.ensureSandbox(ctx, s, u)
}

var errNoSandbox = errors.New("nothing has run in this session yet, so there are no files")

func (a *App) fileExec(ctx context.Context, s *store.ChatSession, cid string, spec sandbox.ExecSpec) (sandbox.ExecResult, error) {
	if spec.WorkingDir == "" {
		spec.WorkingDir = "/"
	}
	if spec.Timeout == 0 {
		spec.Timeout = 60 * time.Second
	}
	return a.Sandbox.Exec(ctx, s.ID, cid, spec)
}

// filePath reads the path parameter and resolves it inside the workspace.
func filePath(w http.ResponseWriter, p string) (string, bool) {
	if p == "" {
		p = sandbox.WorkspacePath
	}
	sp, err := agent.SafePath(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return sp, true
}

// fileSession finds the session and its sandbox for a file request.
func (a *App) fileSession(w http.ResponseWriter, r *http.Request, create bool) (*store.ChatSession, *store.User, string, bool) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return nil, nil, "", false
	}
	u := a.user(r)
	cid, err := a.sessionSandbox(r.Context(), s, u, create)
	switch {
	case errors.Is(err, errNoSandbox):
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusConflict, err.Error())
			return nil, nil, "", false
		}
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": err.Error()})
		return nil, nil, "", false
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return nil, nil, "", false
	}
	return s, u, cid, true
}

// writableSession is a fileSession for requests that change files: refused while the agent is
// working (it would race with the agent's own edits) and when the workspace is full.
func (a *App) writableSession(w http.ResponseWriter, r *http.Request, create bool) (*store.ChatSession, *store.User, string, bool) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return nil, nil, "", false
	}
	if run := a.Runs.Get(s.ID); run != nil && run.IsRunning() {
		writeErr(w, http.StatusConflict, "The agent is working in these files. Stop it or wait until it has finished.")
		return nil, nil, "", false
	}
	u := a.user(r)
	if full, used, limit := a.workspaceFull(r.Context(), u.ID, s.ID); full {
		writeErr(w, http.StatusInsufficientStorage, workspaceFullMessage(used, limit))
		return nil, nil, "", false
	}
	return a.fileSession(w, r, create)
}

const listScript = `cd "$1" 2>/dev/null || exit 3
find . -mindepth 1 -maxdepth 1 ! -name .syntaxis-shadow -printf '%y\t%s\t%T@\t%f\n' | head -n ` + "2001"

// handleFileList lists one directory of the workspace.
func (a *App) handleFileList(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.fileSession(w, r, false)
	if !ok {
		return
	}
	dir, ok := filePath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	res, err := a.fileExec(r.Context(), s, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", listScript, "_", dir}})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list the folder: "+err.Error())
		return
	}
	if res.ExitCode == 3 {
		writeErr(w, http.StatusNotFound, "that folder does not exist")
		return
	}
	entries := parseListing(res.Stdout)
	truncated := len(entries) > maxListEntries
	if truncated {
		entries = entries[:maxListEntries]
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "path": dir, "entries": entries, "truncated": truncated})
}

func parseListing(out string) []FileEntry {
	entries := []FileEntry{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			continue
		}
		e := FileEntry{Name: f[3]}
		switch f[0] {
		case "d":
			e.Type = "dir"
		case "f":
			e.Type = "file"
		case "l":
			e.Type = "link"
		default:
			e.Type = "other"
		}
		e.Size, _ = strconv.ParseInt(f[1], 10, 64)
		if secs, err := strconv.ParseFloat(f[2], 64); err == nil {
			e.Modified = time.Unix(int64(secs), 0).UTC()
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].Type == "dir", entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

// readFileBytes reads up to limit bytes of a file (and says whether there was more). Content is
// moved as base64 so any bytes survive.
func (a *App) readFileBytes(ctx context.Context, s *store.ChatSession, cid, p string, limit int) (data []byte, size int64, err error) {
	res, err := a.fileExec(ctx, s, cid, sandbox.ExecSpec{
		Cmd:       []string{"sh", "-c", `[ -f "$1" ] || exit 3; wc -c < "$1" | tr -d ' ' >&2; head -c "$2" -- "$1" | base64 -w0`, "_", p, strconv.Itoa(limit)},
		MaxOutput: limit*4/3 + 4096,
	})
	if err != nil {
		return nil, 0, err
	}
	if res.ExitCode == 3 {
		return nil, 0, errNotAFile
	}
	if res.ExitCode != 0 {
		return nil, 0, fmt.Errorf("cannot read the file: %s", strings.TrimSpace(res.Stderr))
	}
	size, _ = strconv.ParseInt(strings.TrimSpace(res.Stderr), 10, 64)
	data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(res.Stdout))
	if err != nil {
		return nil, 0, errors.New("the file could not be read completely")
	}
	return data, size, nil
}

var errNotAFile = errors.New("that is not a file")

func isText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	probe := b
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	for _, c := range probe {
		if c == 0 {
			return false
		}
	}
	// a cut in the middle of a character is not a reason to call a file binary
	for len(probe) > 0 && !utf8.Valid(probe) && len(probe) > 8000-4 {
		probe = probe[:len(probe)-1]
	}
	return utf8.Valid(probe)
}

// handleFileGet returns a file as text for the viewer and editor.
func (a *App) handleFileGet(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.fileSession(w, r, false)
	if !ok {
		return
	}
	p, ok := filePath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	data, size, err := a.readFileBytes(r.Context(), s, cid, p, maxViewBytes)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errNotAFile) {
			code = http.StatusNotFound
		}
		writeErr(w, code, err.Error())
		return
	}
	if !isText(data) {
		writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": size, "binary": true})
		return
	}
	// the file is shown as it is: it is the user's own workspace, and an editor that saved back a
	// redacted copy would destroy what it hid
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": size, "binary": false, "truncated": size > int64(len(data)), "text": string(data)})
}

// handleFileDownload sends a file as it is.
func (a *App) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.fileSession(w, r, false)
	if !ok {
		return
	}
	p, ok := filePath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	data, size, err := a.readFileBytes(r.Context(), s, cid, p, maxDownload)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if size > int64(len(data)) {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than %d MiB; download it from the terminal instead", maxDownload>>20))
		return
	}
	name := path.Base(p)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeDownloadName(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func safeDownloadName(n string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' || r == '/' || r == 127 {
			return '_'
		}
		return r
	}, n)
}

// writeFileBytes writes a file in the workspace, creating its folders.
func (a *App) writeFileBytes(ctx context.Context, s *store.ChatSession, cid, p string, data []byte) error {
	res, err := a.fileExec(ctx, s, cid, sandbox.ExecSpec{
		Cmd:   []string{"sh", "-c", `[ -d "$1" ] && exit 4; mkdir -p "$(dirname "$1")" && cat > "$1"`, "_", p},
		Stdin: data, Timeout: 2 * time.Minute,
	})
	if err != nil {
		return err
	}
	if res.ExitCode == 4 {
		return errors.New("that is a folder")
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("could not write the file: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// handleFilePut saves text into a file (the editor).
func (a *App) handleFilePut(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.writableSession(w, r, false)
	if !ok {
		return
	}
	var b struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxViewBytes+4096)
	if !decode(w, r, &b) {
		return
	}
	p, ok := filePath(w, b.Path)
	if !ok {
		return
	}
	if p == sandbox.WorkspacePath {
		writeErr(w, http.StatusBadRequest, "choose a file")
		return
	}
	if len(b.Text) > maxViewBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "the file is too large to save from the editor")
		return
	}
	if err := a.writeFileBytes(r.Context(), s, cid, p, []byte(b.Text)); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.fileAudit(r, "file.edit", s.ID, p)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p, "size": len(b.Text)})
}

// cleanUploadName reduces a name from the browser to a plain file name.
func cleanUploadName(n string) (string, bool) {
	n = strings.ReplaceAll(n, "\\", "/")
	n = path.Base(n)
	n = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, n)
	n = strings.TrimSpace(n)
	if n == "" || n == "." || n == ".." || n == "/" || len(n) > 200 {
		return "", false
	}
	return n, true
}

// uploadSettings is what the admin allows for uploads.
type uploadSettings struct {
	enabled  bool
	maxBytes int64
	kinds    map[string]bool
}

func (a *App) uploadLimit(ctx context.Context) uploadSettings {
	mb, err := strconv.Atoi(a.setting(ctx, SettingUploadMaxMB, settingDefaults[SettingUploadMaxMB]))
	if err != nil || mb < 1 || mb > maxUploadMBLimit {
		mb = 10
	}
	kinds, err := uploadcheck.ParseKinds(a.setting(ctx, SettingUploadKinds, settingDefaults[SettingUploadKinds]))
	if err != nil {
		kinds = map[string]bool{}
	}
	return uploadSettings{enabled: a.settingBool(ctx, SettingUploadsEnabled, false), maxBytes: int64(mb) << 20, kinds: kinds}
}

// handleFileUpload stores an uploaded file (the request body) in a folder of the workspace.
func (a *App) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if !a.uploadLimit(r.Context()).enabled {
		writeErr(w, http.StatusForbidden, "file uploads are switched off by the administrator")
		return
	}
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	dir, ok := filePath(w, r.URL.Query().Get("dir"))
	if !ok {
		return
	}
	name, ok := cleanUploadName(r.URL.Query().Get("name"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "the file needs a name")
		return
	}
	limit := a.uploadLimit(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, limit.maxBytes+1)
	data, err := io.ReadAll(r.Body)
	if err != nil || int64(len(data)) > limit.maxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("files can be up to %d MiB", limit.maxBytes>>20))
		return
	}
	if err := uploadcheck.Check(name, data, limit.kinds); err != nil {
		_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", principal(r).Account.ID), "file.upload_refused", "session:"+s.ID, fmt.Sprintf("%s: %v", name, err))
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	// the sandbox is only started for a file that passed the checks
	s, _, cid, ok := a.writableSession(w, r, true)
	if !ok {
		return
	}
	p := path.Join(dir, name)
	if err := a.writeFileBytes(r.Context(), s, cid, p, data); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.fileAudit(r, "file.upload", s.ID, fmt.Sprintf("%s (%d bytes)", p, len(data)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p, "name": name, "size": len(data)})
}

// handleFileDelete removes a file or folder (never the workspace itself).
func (a *App) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.writableSession(w, r, false)
	if !ok {
		return
	}
	p, ok := filePath(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if p == sandbox.WorkspacePath {
		writeErr(w, http.StatusBadRequest, "the workspace itself cannot be deleted")
		return
	}
	res, err := a.fileExec(r.Context(), s, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `rm -rf -- "$1"`, "_", p}})
	if err != nil || res.ExitCode != 0 {
		writeErr(w, http.StatusBadGateway, "could not delete it")
		return
	}
	a.fileAudit(r, "file.delete", s.ID, p)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleFileMkdir creates a folder.
func (a *App) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	s, _, cid, ok := a.writableSession(w, r, true)
	if !ok {
		return
	}
	var b struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, ok := filePath(w, b.Path)
	if !ok {
		return
	}
	res, err := a.fileExec(r.Context(), s, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `mkdir -p -- "$1"`, "_", p}})
	if err != nil || res.ExitCode != 0 {
		writeErr(w, http.StatusBadGateway, "could not create the folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p})
}

func (a *App) fileAudit(r *http.Request, action, session, detail string) {
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", principal(r).Account.ID), action, "session:"+session, detail)
}
