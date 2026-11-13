// SPDX-License-Identifier: GPL-2.0-or-later

// Package lifecycle enforces data-retention policy: stale sandbox containers
// are handled by the sandbox reaper; this package handles workspaces. A
// workspace is deleted after RetentionDays without use, with a warning email
// first and a per-user "keep" flag.
package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Policy configures workspace retention.
type Policy struct {
	RetentionDays int // 0 = keep forever
	WarnDays      int // warn this many days before deletion (default 3)
}

// Job runs retention.
type Job struct {
	Store   store.Store
	Runtime sandbox.Runtime
	Mail    mail.Mailer
	Policy  Policy
	BaseURL string
	Now     func() time.Time
}

// Result summarises a pass.
type Result struct {
	Warned, Deleted []int64
}

func keepKey(uid int64) string   { return "ws_keep:" + strconv.FormatInt(uid, 10) }
func warnedKey(uid int64) string { return "ws_warned:" + strconv.FormatInt(uid, 10) }

// Keep marks a workspace as kept (user pressed "keep"); it also resets the clock.
func (j *Job) Keep(ctx context.Context, uid int64, keep bool) error {
	v := "false"
	if keep {
		v = "true"
	}
	_ = j.Store.SetSetting(ctx, warnedKey(uid), "")
	return j.Store.SetSetting(ctx, keepKey(uid), v)
}

// lastUsed is the latest activity among the user's sessions (or account creation).
func (j *Job) lastUsed(ctx context.Context, u store.User) time.Time {
	last := u.CreatedAt
	ss, _ := j.Store.ListChatSessions(ctx, u.ID)
	for _, s := range ss {
		if s.UpdatedAt.After(last) {
			last = s.UpdatedAt
		}
	}
	return last
}

// Run performs one pass over all users.
func (j *Job) Run(ctx context.Context) (Result, error) {
	var res Result
	if j.Policy.RetentionDays <= 0 {
		return res, nil
	}
	now := time.Now()
	if j.Now != nil {
		now = j.Now()
	}
	warn := j.Policy.WarnDays
	if warn <= 0 {
		warn = 3
	}
	day := 24 * time.Hour
	retention := time.Duration(j.Policy.RetentionDays) * day
	users, err := j.Store.ListUsers(ctx)
	if err != nil {
		return res, err
	}
	for _, u := range users {
		if keep, _ := j.Store.GetSetting(ctx, keepKey(u.ID), "false"); keep == "true" {
			continue
		}
		idle := now.Sub(j.lastUsed(ctx, u))
		switch {
		case idle >= retention:
			if running, _ := j.hasLiveSandbox(ctx, u.ID); running {
				continue // never delete under a running sandbox
			}
			if err := j.Runtime.RemoveVolume(ctx, sandbox.WorkspaceVolume(u.ID)); err != nil && err != sandbox.ErrNotFound {
				continue
			}
			_, _ = j.Store.AppendAudit(ctx, "system", "workspace.delete", fmt.Sprintf("user:%d", u.ID), fmt.Sprintf("unused for %d days", int(idle/day)))
			_ = j.Store.SetSetting(ctx, warnedKey(u.ID), "")
			res.Deleted = append(res.Deleted, u.ID)
		case idle >= retention-time.Duration(warn)*day:
			if w, _ := j.Store.GetSetting(ctx, warnedKey(u.ID), ""); w != "" {
				continue // already warned
			}
			left := int((retention - idle + day - 1) / day)
			body := fmt.Sprintf("Hello %s,\n\nYour Syntaxis workspace has not been used for a while and will be deleted in about %d day(s). Open Syntaxis and use it, or press Keep, to stop this. Export anything you need first.\n\n%s\n", u.Name, left, j.BaseURL)
			if err := j.Mail.Send(ctx, mail.Message{To: u.Email, Subject: "Your Syntaxis workspace will be deleted soon", Body: body}); err != nil {
				continue
			}
			_ = j.Store.SetSetting(ctx, warnedKey(u.ID), strconv.FormatInt(now.Unix(), 10))
			res.Warned = append(res.Warned, u.ID)
		}
	}
	return res, nil
}

func (j *Job) hasLiveSandbox(ctx context.Context, uid int64) (bool, error) {
	cs, err := j.Runtime.List(ctx)
	if err != nil {
		return true, err // fail safe: assume live
	}
	for _, c := range cs {
		if c.Labels[sandbox.LabelUser] == strconv.FormatInt(uid, 10) && (c.Running || c.Paused) {
			return true, nil
		}
	}
	return false, nil
}
