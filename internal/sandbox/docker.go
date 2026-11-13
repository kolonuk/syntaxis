// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Docker implements Runtime on the Docker Engine API. Point it at the
// docker-socket-proxy (tcp://...) rather than the raw socket.
type Docker struct {
	cli *client.Client
}

// NewDocker connects to host (empty = environment defaults).
func NewDocker(host string) (*Docker, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &Docker{cli: c}, nil
}

// Close releases the client.
func (d *Docker) Close() error { return d.cli.Close() }

// Ping implements Runtime.
func (d *Docker) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx)
	return err
}

// HasRuntime implements Runtime.
func (d *Docker) HasRuntime(ctx context.Context, name string) (bool, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return false, err
	}
	_, ok := info.Runtimes[name]
	return ok, nil
}

// ImagePresent implements Runtime.
func (d *Docker) ImagePresent(ctx context.Context, ref string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// EnsureNetwork implements Runtime: an isolated bridge with inter-container
// communication disabled and IPv6 off. Egress rules are applied separately.
func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", name))})
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false",
		},
		Labels: map[string]string{LabelManaged: "true"},
	})
	return err
}

// EnsureVolume implements Runtime.
func (d *Docker) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	_, err := d.cli.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: labels})
	return err
}

// RemoveVolume implements Runtime.
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	err := d.cli.VolumeRemove(ctx, name, false)
	if cerrdefs.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// VolumeUsage implements Runtime (best effort via the disk-usage API).
func (d *Docker) VolumeUsage(ctx context.Context, name string) (int64, error) {
	v, err := d.cli.VolumeInspect(ctx, name)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if v.UsageData != nil {
		return v.UsageData.Size, nil
	}
	return 0, nil
}

// Create implements Runtime.
func (d *Docker) Create(ctx context.Context, s Spec) (string, error) {
	var mounts []mount.Mount
	for _, m := range s.Mounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	init := s.Init
	hc := &container.HostConfig{
		Runtime:        s.Runtime,
		Privileged:     false,
		ReadonlyRootfs: s.ReadOnlyRootFS,
		CapDrop:        s.CapDrop,
		CapAdd:         s.CapAdd,
		SecurityOpt:    s.SecurityOpt,
		Tmpfs:          s.Tmpfs,
		Mounts:         mounts,
		DNS:            s.DNS,
		Init:           &init,
		ShmSize:        s.ShmBytes,
		Resources: container.Resources{
			Memory:     s.MemoryBytes,
			MemorySwap: s.MemorySwap,
			NanoCPUs:   s.NanoCPUs,
			PidsLimit:  &s.PidsLimit,
		},
		NetworkMode: container.NetworkMode(s.Network),
	}
	if s.NetworkDisabled {
		hc.NetworkMode = "none"
	}
	cfg := &container.Config{
		Image:           s.Image,
		User:            s.User,
		Cmd:             s.Cmd,
		Env:             s.Env,
		Labels:          s.Labels,
		WorkingDir:      WorkspacePath,
		NetworkDisabled: false,
	}
	resp, err := d.cli.ContainerCreate(ctx, cfg, hc, nil, nil, s.Name)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func mapNotFound(err error) error {
	if cerrdefs.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// Start implements Runtime.
func (d *Docker) Start(ctx context.Context, id string) error {
	return mapNotFound(d.cli.ContainerStart(ctx, id, container.StartOptions{}))
}

// Stop implements Runtime.
func (d *Docker) Stop(ctx context.Context, id string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	return mapNotFound(d.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &secs}))
}

// Pause implements Runtime.
func (d *Docker) Pause(ctx context.Context, id string) error {
	return mapNotFound(d.cli.ContainerPause(ctx, id))
}

// Unpause implements Runtime.
func (d *Docker) Unpause(ctx context.Context, id string) error {
	return mapNotFound(d.cli.ContainerUnpause(ctx, id))
}

// Remove implements Runtime.
func (d *Docker) Remove(ctx context.Context, id string) error {
	return mapNotFound(d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}))
}

// Exec implements Runtime. Output is capped at e.MaxOutput bytes per stream.
func (d *Docker) Exec(ctx context.Context, id string, e ExecSpec) (ExecResult, error) {
	start := time.Now()
	created, err := d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: e.Cmd, Env: e.Env, WorkingDir: e.WorkingDir, AttachStdout: true, AttachStderr: true, AttachStdin: len(e.Stdin) > 0,
	})
	if err != nil {
		return ExecResult{}, mapNotFound(err)
	}
	att, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, err
	}
	defer att.Close()
	if len(e.Stdin) > 0 {
		go func() { _, _ = att.Conn.Write(e.Stdin); _ = att.CloseWrite() }()
	}
	runCtx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	var so, se capBuffer
	so.max, se.max = e.MaxOutput, e.MaxOutput
	done := make(chan error, 1)
	go func() { _, err := stdcopy.StdCopy(&so, &se, att.Reader); done <- err }()
	res := ExecResult{}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) {
			return res, err
		}
	case <-runCtx.Done():
		res.TimedOut = true
		att.Close()
		// kill whatever is still running from this exec by signalling its process group
		_, _ = d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: []string{"sh", "-c", "pkill -KILL -P 1 -f . 2>/dev/null || true"}})
	}
	insp, err := d.cli.ContainerExecInspect(ctx, created.ID)
	if err == nil && !res.TimedOut {
		res.ExitCode = insp.ExitCode
	}
	if res.TimedOut {
		res.ExitCode = -1
	}
	res.Stdout, res.Stderr = so.buf.String(), se.buf.String()
	res.Truncated = so.dropped || se.dropped
	res.Duration = time.Since(start)
	return res, nil
}

// capBuffer retains at most max bytes, discarding the rest.
type capBuffer struct {
	buf     bytes.Buffer
	max     int
	dropped bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if room <= 0 {
		c.dropped = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.dropped = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// Stats implements Runtime.
func (d *Docker) Stats(ctx context.Context, id string) (Stats, error) {
	r, err := d.cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return Stats{}, mapNotFound(err)
	}
	defer r.Body.Close()
	var v container.StatsResponse
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return Stats{}, err
	}
	st := Stats{MemoryBytes: int64(v.MemoryStats.Usage), MemoryLimit: int64(v.MemoryStats.Limit), Pids: int64(v.PidsStats.Current), At: time.Now()}
	cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(v.CPUStats.SystemUsage) - float64(v.PreCPUStats.SystemUsage)
	if sysDelta > 0 && cpuDelta > 0 {
		n := float64(v.CPUStats.OnlineCPUs)
		if n == 0 {
			n = 1
		}
		st.CPUPercent = cpuDelta / sysDelta * n * 100
	}
	return st, nil
}

// List implements Runtime.
func (d *Docker) List(ctx context.Context) ([]ContainerInfo, error) {
	cs, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))})
	if err != nil {
		return nil, err
	}
	var out []ContainerInfo
	for _, c := range cs {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
			if len(name) > 0 && name[0] == '/' {
				name = name[1:]
			}
		}
		out = append(out, ContainerInfo{ID: c.ID, Name: name, Labels: c.Labels, Running: c.State == "running", Paused: c.State == "paused", Created: time.Unix(c.Created, 0)})
	}
	return out, nil
}

// Inspect implements Runtime.
func (d *Docker) Inspect(ctx context.Context, id string) (*ContainerInfo, error) {
	c, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, mapNotFound(err)
	}
	created, _ := time.Parse(time.RFC3339Nano, c.Created)
	ci := &ContainerInfo{ID: c.ID, Name: c.Name, Created: created}
	if c.Config != nil {
		ci.Labels = c.Config.Labels
	}
	if c.State != nil {
		ci.Running, ci.Paused = c.State.Running, c.State.Paused
	}
	return ci, nil
}

// ContainerIP implements Runtime.
func (d *Docker) ContainerIP(ctx context.Context, id, network string) (string, error) {
	c, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", mapNotFound(err)
	}
	if c.NetworkSettings != nil {
		if n, ok := c.NetworkSettings.Networks[network]; ok && n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	return "", ErrNotFound
}

var _ Runtime = (*Docker)(nil)

// ParseCreated reads the created label as a time (reaper helper).
func ParseCreated(labels map[string]string) (time.Time, bool) {
	n, err := strconv.ParseInt(labels[LabelCreated], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

var _ = fmt.Sprintf
