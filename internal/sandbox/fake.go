// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Runtime for tests. Volumes hold files; a container sees
// only the volumes mounted into it, which lets tests prove isolation logic.
type Fake struct {
	mu         sync.Mutex
	Runtimes   map[string]bool
	Images     map[string]bool
	Networks   map[string]bool
	Volumes    map[string]map[string]string // volume -> path -> content
	Containers map[string]*FakeContainer
	next       int
	ExecHook   func(c *FakeContainer, e ExecSpec) (ExecResult, bool)
	CreateErr  error
	IPs        map[string]string
	Now        func() time.Time
}

// FakeContainer is a fake container.
type FakeContainer struct {
	Info   ContainerInfo
	Spec   Spec
	Execs  []ExecSpec
	StatsV Stats
}

// NewFake returns a Fake with runsc registered and the default image present.
func NewFake() *Fake {
	return &Fake{Runtimes: map[string]bool{"runsc": true}, Images: map[string]bool{"syntaxis-sandbox:latest": true},
		Networks: map[string]bool{}, Volumes: map[string]map[string]string{}, Containers: map[string]*FakeContainer{}, Now: time.Now}
}

// Ping implements Runtime.
func (f *Fake) Ping(context.Context) error { return nil }

// HasRuntime implements Runtime.
func (f *Fake) HasRuntime(_ context.Context, n string) (bool, error) { return f.Runtimes[n], nil }

// ImagePresent implements Runtime.
func (f *Fake) ImagePresent(_ context.Context, i string) (bool, error) { return f.Images[i], nil }

// EnsureNetwork implements Runtime.
func (f *Fake) EnsureNetwork(_ context.Context, n string) error {
	f.mu.Lock()
	f.Networks[n] = true
	f.mu.Unlock()
	return nil
}

// EnsureVolume implements Runtime.
func (f *Fake) EnsureVolume(_ context.Context, n string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Volumes[n] == nil {
		f.Volumes[n] = map[string]string{}
	}
	return nil
}

// RemoveVolume implements Runtime.
func (f *Fake) RemoveVolume(_ context.Context, n string) error {
	f.mu.Lock()
	delete(f.Volumes, n)
	f.mu.Unlock()
	return nil
}

// VolumeUsage implements Runtime.
func (f *Fake) VolumeUsage(_ context.Context, n string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t int64
	for _, c := range f.Volumes[n] {
		t += int64(len(c))
	}
	return t, nil
}

// Create implements Runtime.
func (f *Fake) Create(_ context.Context, s Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	f.next++
	id := fmt.Sprintf("c%04d", f.next)
	f.Containers[id] = &FakeContainer{Spec: s, Info: ContainerInfo{ID: id, Name: s.Name, Labels: s.Labels, Created: f.Now()}}
	return id, nil
}

func (f *Fake) get(id string) (*FakeContainer, error) {
	c, ok := f.Containers[id]
	if !ok {
		return nil, ErrNotFound
	}
	return c, nil
}

// Start implements Runtime.
func (f *Fake) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err == nil {
		c.Info.Running, c.Info.Paused = true, false
	}
	return err
}

// Stop implements Runtime.
func (f *Fake) Stop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err == nil {
		c.Info.Running, c.Info.Paused = false, false
	}
	return err
}

// Pause implements Runtime.
func (f *Fake) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err == nil {
		c.Info.Paused = true
	}
	return err
}

// Unpause implements Runtime.
func (f *Fake) Unpause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err == nil {
		c.Info.Paused = false
	}
	return err
}

// Remove implements Runtime.
func (f *Fake) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(id); err != nil {
		return err
	}
	delete(f.Containers, id)
	return nil
}

// Exec implements Runtime. Built-in commands operate on mounted volumes:
// `cat PATH`, `write PATH CONTENT`, `ls`. A ExecHook can override.
func (f *Fake) Exec(_ context.Context, id string, e ExecSpec) (ExecResult, error) {
	f.mu.Lock()
	c, err := f.get(id)
	if err != nil {
		f.mu.Unlock()
		return ExecResult{}, err
	}
	c.Execs = append(c.Execs, e)
	hook := f.ExecHook
	f.mu.Unlock()
	if hook != nil {
		if r, ok := hook(c, e); ok {
			return r, nil
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	vol := func(path string) (map[string]string, string, bool) {
		for _, m := range c.Spec.Mounts {
			if strings.HasPrefix(path, m.Target+"/") {
				return f.Volumes[m.Volume], strings.TrimPrefix(path, m.Target+"/"), true
			}
		}
		return nil, "", false
	}
	if len(e.Cmd) >= 2 {
		switch e.Cmd[0] {
		case "cat":
			v, rel, ok := vol(e.Cmd[1])
			if !ok {
				return ExecResult{ExitCode: 1, Stderr: "cat: " + e.Cmd[1] + ": No such file or directory"}, nil
			}
			if content, ok := v[rel]; ok {
				return ExecResult{Stdout: content}, nil
			}
			return ExecResult{ExitCode: 1, Stderr: "cat: " + e.Cmd[1] + ": No such file or directory"}, nil
		case "write":
			if len(e.Cmd) < 3 {
				return ExecResult{ExitCode: 2, Stderr: "usage"}, nil
			}
			v, rel, ok := vol(e.Cmd[1])
			if !ok {
				return ExecResult{ExitCode: 1, Stderr: "read-only file system"}, nil
			}
			v[rel] = e.Cmd[2]
			return ExecResult{}, nil
		}
	}
	return ExecResult{Stdout: strings.Join(e.Cmd, " ")}, nil
}

// Stats implements Runtime.
func (f *Fake) Stats(_ context.Context, id string) (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return Stats{}, err
	}
	return c.StatsV, nil
}

// List implements Runtime.
func (f *Fake) List(context.Context) ([]ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerInfo
	for _, c := range f.Containers {
		out = append(out, c.Info)
	}
	return out, nil
}

// Inspect implements Runtime.
func (f *Fake) Inspect(_ context.Context, id string) (*ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return nil, err
	}
	ci := c.Info
	return &ci, nil
}

// ContainerIP implements Runtime; tests set IPs[id].
func (f *Fake) ContainerIP(_ context.Context, id, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(id); err != nil {
		return "", err
	}
	if ip, ok := f.IPs[id]; ok {
		return ip, nil
	}
	return "", ErrNotFound
}

var _ Runtime = (*Fake)(nil)
