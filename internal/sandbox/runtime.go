// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"errors"
	"time"
)

// Errors.
var (
	ErrNotFound = errors.New("sandbox: not found")
	ErrTimeout  = errors.New("sandbox: command timed out")
)

// ContainerInfo describes an existing container.
type ContainerInfo struct {
	ID      string
	Name    string
	Labels  map[string]string
	Running bool
	Paused  bool
	Created time.Time
}

// ExecSpec is a command to run in a container.
type ExecSpec struct {
	Cmd        []string
	Env        []string
	WorkingDir string
	Stdin      []byte
	Timeout    time.Duration
	MaxOutput  int // bytes retained (head); 0 = default
}

// ExecResult is a finished command.
type ExecResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Truncated bool
	TimedOut  bool
	Duration  time.Duration
}

// Stats is a point-in-time resource sample.
type Stats struct {
	CPUPercent     float64 // of one core (100 = one full core)
	MemoryBytes    int64
	MemoryLimit    int64
	Pids           int64
	PidsLimit      int64
	BlockWriteRate float64 // bytes/sec since last sample
	At             time.Time
}

// Runtime abstracts the container engine. The Docker implementation talks to
// the Docker API through the socket proxy; tests use Fake.
type Runtime interface {
	Ping(ctx context.Context) error
	// HasRuntime reports whether the named OCI runtime (runsc) is registered.
	HasRuntime(ctx context.Context, name string) (bool, error)
	ImagePresent(ctx context.Context, image string) (bool, error)
	EnsureNetwork(ctx context.Context, name string) error
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeUsage(ctx context.Context, name string) (int64, error)
	Create(ctx context.Context, s Spec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Pause(ctx context.Context, id string) error
	Unpause(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, e ExecSpec) (ExecResult, error)
	Stats(ctx context.Context, id string) (Stats, error)
	// List returns containers carrying the managed label.
	List(ctx context.Context) ([]ContainerInfo, error)
	Inspect(ctx context.Context, id string) (*ContainerInfo, error)
}
