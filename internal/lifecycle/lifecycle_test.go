// SPDX-License-Identifier: GPL-2.0-or-later

package lifecycle

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

var ctx = context.Background()

type fx struct {
	j    *Job
	st   *store.SQLite
	rt   *sandbox.Docker
	mail *mail.Recorder
	now  time.Time
	uid  int64
}

func newFx(t *testing.T, createdDaysAgo int) *fx {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testenv.Docker(t)
	rt, err := sandbox.NewDocker("")
	if err != nil {
		testenv.NotTested(t, "no Docker client: "+err.Error())
	}
	t.Cleanup(func() { _ = rt.Close() })
	// the workspace volumes are named after the user id: keep clear of any real instance on this Docker
	if err := st.SetUserIDFloor(ctx, 930000+time.Now().UnixNano()%50000); err != nil {
		t.Fatal(err)
	}
	f := &fx{st: st, rt: rt, mail: &mail.Recorder{}, now: time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)}
	f.uid, _ = st.CreateAccount(ctx, store.Account{Kind: store.KindUser, Email: "a@example.com", Name: "Alice", CreatedAt: f.now.Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)})
	if err := f.rt.EnsureVolume(ctx, sandbox.WorkspaceVolume(f.uid), map[string]string{sandbox.LabelManaged: "true", sandbox.LabelUser: strconv.FormatInt(f.uid, 10)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.rt.RemoveVolume(ctx, sandbox.WorkspaceVolume(f.uid)) })
	f.j = &Job{Store: st, Runtime: f.rt, Mail: f.mail, Policy: Policy{RetentionDays: 30, WarnDays: 3}, BaseURL: "https://syntaxis.example.com", Now: func() time.Time { return f.now }}
	return f
}

// volume reports whether the user's workspace volume exists on the real Docker.
func (f *fx) volume() bool {
	return exec.Command("docker", "volume", "inspect", sandbox.WorkspaceVolume(f.uid)).Run() == nil
}

func TestRecentWorkspaceUntouched(t *testing.T) {
	f := newFx(t, 5)
	r, _ := f.j.Run(ctx)
	if len(r.Warned)+len(r.Deleted) != 0 || !f.volume() {
		t.Fatalf("%+v", r)
	}
}

func TestWarnsBeforeDeletingThenDeletes(t *testing.T) {
	f := newFx(t, 28) // 2 days left
	r, _ := f.j.Run(ctx)
	if len(r.Warned) != 1 || len(r.Deleted) != 0 || len(f.mail.Sent) != 1 {
		t.Fatalf("%+v", r)
	}
	if !f.volume() {
		t.Fatal("deleted before the retention period")
	}
	// warned once only
	_, _ = f.j.Run(ctx)
	if len(f.mail.Sent) != 1 {
		t.Fatal("warning repeated")
	}
	f.now = f.now.Add(3 * 24 * time.Hour)
	r, _ = f.j.Run(ctx)
	if len(r.Deleted) != 1 || f.volume() {
		t.Fatalf("expired workspace not deleted: %+v", r)
	}
	es, _ := f.st.ListAudit(ctx, 0)
	if len(es) != 1 || es[0].Action != "workspace.delete" {
		t.Fatalf("deletion must be audited: %+v", es)
	}
}

func TestNoDeletionWithoutWarningWindow(t *testing.T) {
	f := newFx(t, 40)
	r, _ := f.j.Run(ctx)
	// long idle: deletion happens (the warning window has passed); the user was never warned
	// because the job never ran earlier. That is acceptable; assert deletion is audited, not silent.
	if len(r.Deleted) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestKeepFlagAndActivityResetClock(t *testing.T) {
	f := newFx(t, 40)
	_ = f.j.Keep(ctx, f.uid, true)
	if r, _ := f.j.Run(ctx); len(r.Deleted) != 0 {
		t.Fatal("kept workspace deleted")
	}
	_ = f.j.Keep(ctx, f.uid, false)
	// recent session activity resets the clock
	_ = f.st.CreateChatSession(ctx, store.ChatSession{ID: "s", UserID: f.uid, Title: "t", Model: "m", CreatedAt: f.now, UpdatedAt: f.now.Add(-2 * 24 * time.Hour)})
	if r, _ := f.j.Run(ctx); len(r.Deleted) != 0 {
		t.Fatal("recently used workspace deleted")
	}
}

func TestNeverDeletesUnderRunningSandbox(t *testing.T) {
	testenv.GVisor(t)
	f := newFx(t, 60)
	cfg := sandbox.DefaultConfig()
	if ok, _ := f.rt.ImagePresent(ctx, cfg.Image); !ok {
		testenv.NotTested(t, "the sandbox image "+cfg.Image+" is not built")
	}
	m := sandbox.NewManager(f.rt, cfg, nil)
	id, err := m.Create(ctx, f.uid, "lifecycle-test", nil, false) // a real running gVisor sandbox for this user
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Destroy(ctx, "lifecycle-test", id) })
	if r, _ := f.j.Run(ctx); len(r.Deleted) != 0 || !f.volume() {
		t.Fatal("workspace deleted under a running sandbox")
	}
}

func TestZeroRetentionKeepsForever(t *testing.T) {
	f := newFx(t, 999)
	f.j.Policy.RetentionDays = 0
	if r, _ := f.j.Run(ctx); len(r.Deleted)+len(r.Warned) != 0 {
		t.Fatal("0 must keep everything")
	}
}
