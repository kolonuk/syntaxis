// SPDX-License-Identifier: GPL-2.0-or-later

// Package checkpoint snapshots a workspace into a hidden shadow git
// repository (a separate --git-dir inside the workspace volume), so the
// user's own .git, history and remotes are never touched. A checkpoint costs
// disk, never tokens: it is not sent to the model.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// ShadowDirName is the shadow repo directory inside the workspace.
const ShadowDirName = ".syntaxis-shadow"

// DefaultIgnore are paths never snapshotted (large/derived directories).
var DefaultIgnore = []string{
	".git/", ShadowDirName + "/", "node_modules/", "vendor/", "target/", "dist/", "build/", ".next/", ".cache/", "__pycache__/", "*.pyc", ".venv/", "venv/", ".gradle/", ".terraform/",
}

// Runner executes a command (argv) and returns stdout. Production runs it in
// the sandbox; tests run it on the local machine.
type Runner func(ctx context.Context, argv []string, env []string) (stdout string, err error)

// Manager manages checkpoints for one workspace.
type Manager struct {
	Run       Runner
	Workspace string   // e.g. /workspace
	Ignore    []string // extra ignore patterns
	MaxCount  int      // keep at most this many (0 = unlimited)
	MaxBytes  int64    // per-user checkpoint disk budget (0 = unlimited)
	Now       func() time.Time
}

// Checkpoint describes one snapshot.
type Checkpoint struct {
	Seq    int
	Commit string
	Label  string
	Time   time.Time
}

// ErrNotFound is returned for an unknown checkpoint.
var ErrNotFound = errors.New("checkpoint: not found")

func (m *Manager) gitDir() string { return path.Join(m.Workspace, ShadowDirName, "git") }

func (m *Manager) git(ctx context.Context, args ...string) (string, error) {
	argv := append([]string{"git", "--git-dir=" + m.gitDir(), "--work-tree=" + m.Workspace}, args...)
	out, err := m.Run(ctx, argv, []string{"GIT_AUTHOR_NAME=syntaxis", "GIT_AUTHOR_EMAIL=checkpoint@syntaxis.invalid", "GIT_COMMITTER_NAME=syntaxis", "GIT_COMMITTER_EMAIL=checkpoint@syntaxis.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"})
	return strings.TrimRight(out, "\n"), err
}

// Init creates the shadow repository if needed and writes the ignore list.
func (m *Manager) Init(ctx context.Context) error {
	if _, err := m.Run(ctx, []string{"mkdir", "-p", m.gitDir()}, nil); err != nil {
		return err
	}
	if _, err := m.git(ctx, "rev-parse", "--git-dir"); err != nil {
		if _, err := m.git(ctx, "init", "-q"); err != nil {
			return fmt.Errorf("init shadow repo: %w", err)
		}
	}
	patterns := append(append([]string{}, DefaultIgnore...), m.Ignore...)
	exclude := path.Join(m.gitDir(), "info", "exclude")
	if _, err := m.Run(ctx, []string{"mkdir", "-p", path.Dir(exclude)}, nil); err != nil {
		return err
	}
	// patterns are passed as separate args to avoid quoting problems
	argv := append([]string{"sh", "-c", `f="$1"; shift; printf '%s\n' "$@" > "$f"`, "_", exclude}, patterns...)
	_, err := m.Run(ctx, argv, nil)
	return err
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Snapshot captures the workspace (tracked and untracked files, minus the
// ignore list) and returns the checkpoint.
func (m *Manager) Snapshot(ctx context.Context, label string) (Checkpoint, error) {
	if err := m.Init(ctx); err != nil {
		return Checkpoint{}, err
	}
	if _, err := m.git(ctx, "add", "-A", "--ignore-errors"); err != nil {
		return Checkpoint{}, fmt.Errorf("stage: %w", err)
	}
	tree, err := m.git(ctx, "write-tree")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("write-tree: %w", err)
	}
	// independent root commits: each checkpoint is a full snapshot (git
	// deduplicates objects), so pruning one never rewrites others.
	label = strings.ReplaceAll(strings.TrimSpace(label), "\n", " ")
	commit, err := m.git(ctx, "commit-tree", tree, "-m", label+"\n\nts="+strconv.FormatInt(m.now().Unix(), 10))
	if err != nil {
		return Checkpoint{}, fmt.Errorf("commit-tree: %w", err)
	}
	seq, err := m.nextSeq(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := m.git(ctx, "update-ref", fmt.Sprintf("refs/cp/%d", seq), commit); err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{Seq: seq, Commit: commit, Label: label, Time: m.now()}
	if err := m.Prune(ctx); err != nil {
		return cp, fmt.Errorf("prune: %w", err)
	}
	return cp, nil
}

func (m *Manager) nextSeq(ctx context.Context) (int, error) {
	cps, err := m.List(ctx)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, c := range cps {
		if c.Seq > max {
			max = c.Seq
		}
	}
	// also consult a persistent counter so pruned numbers are never reused
	out, _ := m.git(ctx, "config", "--get", "syntaxis.lastseq")
	if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && n > max {
		max = n
	}
	next := max + 1
	_, _ = m.git(ctx, "config", "syntaxis.lastseq", strconv.Itoa(next))
	return next, nil
}

// List returns checkpoints, oldest first.
func (m *Manager) List(ctx context.Context) ([]Checkpoint, error) {
	out, err := m.git(ctx, "for-each-ref", "--format=%(refname:short)|%(objectname)|%(contents:subject)|%(contents:body)", "refs/cp/")
	if err != nil {
		return nil, nil // no shadow repo yet
	}
	var cps []Checkpoint
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 3 {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimPrefix(parts[0], "cp/"))
		if err != nil {
			continue
		}
		cp := Checkpoint{Seq: seq, Commit: parts[1], Label: parts[2]}
		if len(parts) == 4 {
			if i := strings.Index(parts[3], "ts="); i >= 0 {
				if ts, err := strconv.ParseInt(strings.TrimSpace(parts[3][i+3:]), 10, 64); err == nil {
					cp.Time = time.Unix(ts, 0)
				}
			}
		}
		cps = append(cps, cp)
	}
	for i := 1; i < len(cps); i++ { // insertion sort by seq
		for j := i; j > 0 && cps[j].Seq < cps[j-1].Seq; j-- {
			cps[j], cps[j-1] = cps[j-1], cps[j]
		}
	}
	return cps, nil
}

func (m *Manager) find(ctx context.Context, seq int) (Checkpoint, error) {
	cps, _ := m.List(ctx)
	for _, c := range cps {
		if c.Seq == seq {
			return c, nil
		}
	}
	return Checkpoint{}, ErrNotFound
}

// Diff returns the diff between a checkpoint and the current workspace.
func (m *Manager) Diff(ctx context.Context, seq int) (string, error) {
	c, err := m.find(ctx, seq)
	if err != nil {
		return "", err
	}
	if _, err := m.git(ctx, "add", "-A", "--ignore-errors"); err != nil {
		return "", err
	}
	tree, err := m.git(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return m.git(ctx, "diff", "--no-color", c.Commit, tree)
}

// DiffNames lists changed files (name-status) since a checkpoint.
func (m *Manager) DiffNames(ctx context.Context, seq int) (string, error) {
	c, err := m.find(ctx, seq)
	if err != nil {
		return "", err
	}
	if _, err := m.git(ctx, "add", "-A", "--ignore-errors"); err != nil {
		return "", err
	}
	tree, err := m.git(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return m.git(ctx, "diff", "--no-color", "--name-status", c.Commit, tree)
}

// Restore resets the workspace files to a checkpoint: files that exist in it
// are restored, files created since are removed. Ignored directories
// (node_modules etc.) and the user's .git are left alone. External effects
// (pushes, deploys) are not undone.
func (m *Manager) Restore(ctx context.Context, seq int) error {
	c, err := m.find(ctx, seq)
	if err != nil {
		return err
	}
	// take a safety snapshot first so a restore is itself undoable
	if _, err := m.Snapshot(ctx, "before restore to checkpoint "+strconv.Itoa(seq)); err != nil {
		return err
	}
	if _, err := m.git(ctx, "read-tree", "--reset", c.Commit); err != nil {
		return err
	}
	if _, err := m.git(ctx, "checkout-index", "-a", "-f"); err != nil {
		return err
	}
	// remove files not in the checkpoint (respecting the exclude list)
	if _, err := m.git(ctx, "clean", "-fdq"); err != nil {
		return err
	}
	return nil
}

// Prune enforces MaxCount (oldest first) and MaxBytes.
func (m *Manager) Prune(ctx context.Context) error {
	cps, _ := m.List(ctx)
	removed := false
	del := func(c Checkpoint) error {
		_, err := m.git(ctx, "update-ref", "-d", fmt.Sprintf("refs/cp/%d", c.Seq))
		removed = true
		return err
	}
	for m.MaxCount > 0 && len(cps) > m.MaxCount {
		if err := del(cps[0]); err != nil {
			return err
		}
		cps = cps[1:]
	}
	if removed {
		_, _ = m.git(ctx, "gc", "--prune=now", "-q")
	}
	if m.MaxBytes > 0 {
		for len(cps) > 1 {
			size, err := m.SizeBytes(ctx)
			if err != nil || size <= m.MaxBytes {
				break
			}
			if err := del(cps[0]); err != nil {
				return err
			}
			cps = cps[1:]
			_, _ = m.git(ctx, "gc", "--prune=now", "-q")
		}
	}
	return nil
}

// SizeBytes returns the shadow repository's disk usage.
func (m *Manager) SizeBytes(ctx context.Context) (int64, error) {
	out, err := m.Run(ctx, []string{"du", "-sk", m.gitDir()}, nil)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, errors.New("unexpected du output")
	}
	kb, err := strconv.ParseInt(f[0], 10, 64)
	return kb * 1024, err
}
