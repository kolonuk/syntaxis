// SPDX-License-Identifier: GPL-2.0-or-later

package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

// localRunner runs argv on the test machine (real git, real filesystem).
func localRunner(dir string) Runner {
	return func(ctx context.Context, argv []string, env []string) (string, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return out.String(), fmt.Errorf("%w: %s", err, errb.String())
		}
		return out.String(), nil
	}
}

func need(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	return string(b), err == nil
}

func setup(t *testing.T) (*Manager, string) {
	need(t)
	ws := t.TempDir()
	return &Manager{Run: localRunner(ws), Workspace: ws}, ws
}

func TestSnapshotListDiffRestore(t *testing.T) {
	m, ws := setup(t)
	write(t, ws, "a.txt", "one")
	write(t, ws, "dir/b.txt", "bee")
	cp1, err := m.Snapshot(ctx, "turn 1")
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws, "a.txt", "ONE changed")
	_ = os.Remove(filepath.Join(ws, "dir/b.txt"))
	write(t, ws, "new.txt", "created later")
	cp2, err := m.Snapshot(ctx, "turn 2")
	if err != nil || cp2.Seq != cp1.Seq+1 {
		t.Fatalf("%+v %v", cp2, err)
	}
	cps, _ := m.List(ctx)
	if len(cps) != 2 || cps[0].Label != "turn 1" || cps[1].Label != "turn 2" || cps[0].Time.IsZero() {
		t.Fatalf("%+v", cps)
	}
	d, err := m.Diff(ctx, cp1.Seq)
	if err != nil || !strings.Contains(d, "ONE changed") || !strings.Contains(d, "new.txt") {
		t.Fatalf("diff: %q %v", d, err)
	}
	names, _ := m.DiffNames(ctx, cp1.Seq)
	if !strings.Contains(names, "D\tdir/b.txt") || !strings.Contains(names, "A\tnew.txt") || !strings.Contains(names, "M\ta.txt") {
		t.Fatalf("%s", names)
	}
	// restore to turn 1: files back, later files gone
	if err := m.Restore(ctx, cp1.Seq); err != nil {
		t.Fatal(err)
	}
	if c, ok := read(t, ws, "a.txt"); !ok || c != "one" {
		t.Fatalf("a.txt = %q", c)
	}
	if c, ok := read(t, ws, "dir/b.txt"); !ok || c != "bee" {
		t.Fatal("deleted file not restored")
	}
	if _, ok := read(t, ws, "new.txt"); ok {
		t.Fatal("file created after the checkpoint survived restore")
	}
	// the restore itself is undoable
	cps, _ = m.List(ctx)
	if !strings.Contains(cps[len(cps)-1].Label, "before restore") {
		t.Fatalf("no safety snapshot: %+v", cps)
	}
	if err := m.Restore(ctx, cps[len(cps)-1].Seq); err != nil {
		t.Fatal(err)
	}
	if c, _ := read(t, ws, "a.txt"); c != "ONE changed" {
		t.Fatalf("undo of restore failed: %q", c)
	}
}

func TestUserGitUntouched(t *testing.T) {
	m, ws := setup(t)
	runE := func(args ...string) (string, error) {
		return localRunner(ws)(ctx, append([]string{"git"}, args...), []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=u", "GIT_AUTHOR_EMAIL=u@example.com", "GIT_COMMITTER_NAME=u", "GIT_COMMITTER_EMAIL=u@example.com"})
	}
	run := func(args ...string) string {
		out, err := runE(args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run("init", "-q")
	write(t, ws, "x.txt", "x")
	run("add", ".")
	run("commit", "-q", "-m", "user commit")
	headBefore := run("rev-parse", "HEAD")
	refsBefore := run("for-each-ref")
	statusBefore := run("status", "--porcelain")

	write(t, ws, "x.txt", "x2")
	for i := 0; i < 3; i++ {
		if _, err := m.Snapshot(ctx, fmt.Sprintf("t%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Restore(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if run("rev-parse", "HEAD") != headBefore || run("for-each-ref") != refsBefore {
		t.Fatal("user's git history or refs changed")
	}
	// restore changed the file contents only; the shadow dir must not be tracked as user-visible noise
	st := run("status", "--porcelain")
	if strings.Contains(st, ShadowDirName) && !strings.Contains(statusBefore, ShadowDirName) {
		// shadow dir shows up as untracked in the user repo status: acceptable but it must not be staged/committed
		if strings.Contains(run("ls-files"), ShadowDirName) {
			t.Fatal("shadow repo leaked into user's index")
		}
	}
	if out, err := runE("config", "--get", "syntaxis.lastseq"); err == nil || out != "" {
		t.Fatal("shadow config leaked into user repo")
	}
}

func TestIgnoreList(t *testing.T) {
	m, ws := setup(t)
	m.Ignore = []string{"*.log"}
	write(t, ws, "src/main.go", "package main")
	write(t, ws, "node_modules/big/index.js", strings.Repeat("x", 1000))
	write(t, ws, "debug.log", "noise")
	write(t, ws, ".git/config", "[core]")
	if _, err := m.Snapshot(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	files, _ := m.git(ctx, "ls-tree", "-r", "--name-only", "refs/cp/1")
	if !strings.Contains(files, "src/main.go") {
		t.Fatalf("source missing: %s", files)
	}
	for _, bad := range []string{"node_modules", "debug.log", ".git/", ShadowDirName} {
		if strings.Contains(files, bad) {
			t.Errorf("ignored path %q snapshotted:\n%s", bad, files)
		}
	}
	// restore must not delete ignored dirs
	write(t, ws, "src/new.go", "package x")
	if err := m.Restore(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := read(t, ws, "node_modules/big/index.js"); !ok {
		t.Fatal("restore deleted an ignored directory")
	}
	if _, ok := read(t, ws, "src/new.go"); ok {
		t.Fatal("new file survived restore")
	}
}

func TestPruneByCount(t *testing.T) {
	m, ws := setup(t)
	m.MaxCount = 3
	for i := 0; i < 6; i++ {
		write(t, ws, "f.txt", fmt.Sprint(i))
		if _, err := m.Snapshot(ctx, fmt.Sprintf("t%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	cps, _ := m.List(ctx)
	if len(cps) != 3 || cps[0].Label != "t3" || cps[2].Label != "t5" {
		t.Fatalf("%+v", cps)
	}
	// sequence numbers are never reused after pruning
	cp, _ := m.Snapshot(ctx, "next")
	if cp.Seq != 7 {
		t.Fatalf("seq reused: %d", cp.Seq)
	}
	// newest checkpoint still restorable after gc
	if err := m.Restore(ctx, cps[2].Seq); err != nil {
		t.Fatal(err)
	}
	if c, _ := read(t, ws, "f.txt"); c != "5" {
		t.Fatalf("%q", c)
	}
}

func TestPruneByBytes(t *testing.T) {
	m, ws := setup(t)
	m.MaxBytes = 1 // tiny budget: keeps only the newest
	for i := 0; i < 3; i++ {
		write(t, ws, "big.bin", strings.Repeat(fmt.Sprint(i), 50000))
		if _, err := m.Snapshot(ctx, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	cps, _ := m.List(ctx)
	if len(cps) != 1 || cps[0].Label != "2" {
		t.Fatalf("budget not enforced, kept %+v", cps)
	}
	if sz, err := m.SizeBytes(ctx); err != nil || sz <= 0 {
		t.Fatalf("size %d %v", sz, err)
	}
}

func TestUnknownCheckpoint(t *testing.T) {
	m, _ := setup(t)
	if _, err := m.Diff(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := m.Restore(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if cps, err := m.List(ctx); err != nil || len(cps) != 0 {
		t.Fatal("list on fresh workspace")
	}
}

func TestLabelNewlineSanitised(t *testing.T) {
	m, ws := setup(t)
	write(t, ws, "a", "a")
	cp, err := m.Snapshot(ctx, "line1\nline2")
	if err != nil || strings.Contains(cp.Label, "\n") {
		t.Fatalf("%+v %v", cp, err)
	}
}

func TestEmptyWorkspaceSnapshot(t *testing.T) {
	m, _ := setup(t)
	if _, err := m.Snapshot(ctx, "empty"); err != nil {
		t.Fatalf("empty workspace: %v", err)
	}
}
