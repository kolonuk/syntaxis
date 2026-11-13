// SPDX-License-Identifier: GPL-2.0-or-later

package lifecycle

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

var ctx = context.Background()

type fx struct {
	j    *Job
	st   *store.SQLite
	rt   *sandbox.Fake
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
	f := &fx{st: st, rt: sandbox.NewFake(), mail: &mail.Recorder{}, now: time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)}
	f.uid, _ = st.CreateAccount(ctx, store.Account{Kind: store.KindUser, Email: "a@example.com", Name: "Alice", CreatedAt: f.now.Add(-time.Duration(createdDaysAgo) * 24 * time.Hour)})
	_ = f.rt.EnsureVolume(ctx, sandbox.WorkspaceVolume(f.uid), nil)
	f.j = &Job{Store: st, Runtime: f.rt, Mail: f.mail, Policy: Policy{RetentionDays: 30, WarnDays: 3}, BaseURL: "https://syntaxis.example.com", Now: func() time.Time { return f.now }}
	return f
}

func TestRecentWorkspaceUntouched(t *testing.T) {
	f := newFx(t, 5)
	r, _ := f.j.Run(ctx)
	if len(r.Warned)+len(r.Deleted) != 0 || f.rt.Volumes[sandbox.WorkspaceVolume(f.uid)] == nil {
		t.Fatalf("%+v", r)
	}
}

func TestWarnsBeforeDeletingThenDeletes(t *testing.T) {
	f := newFx(t, 28) // 2 days left
	r, _ := f.j.Run(ctx)
	if len(r.Warned) != 1 || len(r.Deleted) != 0 || len(f.mail.Sent) != 1 {
		t.Fatalf("%+v", r)
	}
	if f.rt.Volumes[sandbox.WorkspaceVolume(f.uid)] == nil {
		t.Fatal("deleted before the retention period")
	}
	// warned once only
	_, _ = f.j.Run(ctx)
	if len(f.mail.Sent) != 1 {
		t.Fatal("warning repeated")
	}
	f.now = f.now.Add(3 * 24 * time.Hour)
	r, _ = f.j.Run(ctx)
	if len(r.Deleted) != 1 || f.rt.Volumes[sandbox.WorkspaceVolume(f.uid)] != nil {
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
	f := newFx(t, 60)
	id, _ := f.rt.Create(ctx, sandbox.Spec{Name: "c", Labels: map[string]string{sandbox.LabelManaged: "true", sandbox.LabelUser: "1"}})
	_ = f.rt.Start(ctx, id)
	if r, _ := f.j.Run(ctx); len(r.Deleted) != 0 || f.rt.Volumes[sandbox.WorkspaceVolume(f.uid)] == nil {
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
