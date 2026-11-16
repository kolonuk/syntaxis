// SPDX-License-Identifier: GPL-2.0-or-later

// Package dockerproxy is a strict, validating proxy in front of the Docker socket.
//
// Syntaxis needs to create and drive sandbox containers, which takes a socket that
// can also start a privileged container with the host's root filesystem mounted.
// A generic socket proxy that only filters by endpoint still allows that. This proxy
// reads every request that can change the host, refuses anything a sandbox does not
// need, and forwards only bodies it has validated (re-encoded, so nothing it did not
// check reaches the daemon). If the Syntaxis server is compromised, the most it can
// do through this proxy is run more gVisor sandboxes with the same restrictions.
package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Labels and names shared with the sandbox package (kept here to avoid an import cycle
// and so the rules are visible in one place).
const (
	LabelManaged = "io.syntaxis.managed"
	LabelHelper  = "io.syntaxis.helper"
	DNSVolume    = "syntaxis-dns"
)

// volumeName is what Syntaxis may create, mount and remove.
var volumeName = regexp.MustCompile(`^syntaxis-[a-z0-9][a-z0-9_.-]*$`)

// Config configures the proxy.
type Config struct {
	// Network is the sandbox network: the only network containers may join and the
	// only one that may be created.
	Network string
	// Dial connects to the Docker daemon (for example the unix socket).
	Dial func(ctx context.Context) (net.Conn, error)
	Log  *slog.Logger
}

// Proxy is an http.Handler.
type Proxy struct {
	cfg Config
	rp  *httputil.ReverseProxy
	hc  *http.Client
}

// New builds a proxy.
func New(cfg Config) *Proxy {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	tr := &http.Transport{
		DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return cfg.Dial(ctx) },
		MaxIdleConns:    16,
		IdleConnTimeout: 90 * time.Second,
	}
	p := &Proxy{cfg: cfg, hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: "docker"})
			pr.Out.Host = "docker"
		},
		Transport:     tr,
		FlushInterval: -1, // logs, attach and exec stream
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "docker proxy: upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}
	return p
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+(\.[0-9]+)?(/|$)`)

// route is one allowed endpoint. check may validate (and replace) the body.
type route struct {
	method string
	re     *regexp.Regexp
	check  func(p *Proxy, r *http.Request, m []string, body []byte) ([]byte, error)
}

func rt(method, pattern string, check func(p *Proxy, r *http.Request, m []string, body []byte) ([]byte, error)) route {
	return route{method, regexp.MustCompile("^" + pattern + "$"), check}
}

const id = `([a-zA-Z0-9][a-zA-Z0-9_.-]*)`

var routes = []route{
	rt("GET", `/_ping`, nil),
	rt("HEAD", `/_ping`, nil),
	rt("GET", `/version`, nil),
	rt("GET", `/info`, nil),
	rt("GET", `/images/(.+)/json`, nil),

	rt("GET", `/networks`, nil),
	rt("GET", `/networks/`+id, nil),
	rt("POST", `/networks/create`, checkNetworkCreate),

	rt("GET", `/volumes/`+id, nil),
	rt("POST", `/volumes/create`, checkVolumeCreate),
	rt("DELETE", `/volumes/`+id, checkVolumeName),

	rt("GET", `/containers/json`, checkContainerList),
	rt("POST", `/containers/create`, checkContainerCreate),
	rt("GET", `/containers/`+id+`/json`, checkManaged),
	rt("GET", `/containers/`+id+`/stats`, checkManaged),
	rt("GET", `/containers/`+id+`/logs`, checkManaged),
	rt("POST", `/containers/`+id+`/start`, checkStart),
	rt("POST", `/containers/`+id+`/stop`, checkManagedOrHelper),
	rt("POST", `/containers/`+id+`/pause`, checkManaged),
	rt("POST", `/containers/`+id+`/unpause`, checkManaged),
	rt("POST", `/containers/`+id+`/update`, checkUpdate),
	rt("POST", `/containers/`+id+`/attach`, checkManaged),
	rt("POST", `/containers/`+id+`/exec`, checkExecCreate),
	rt("PUT", `/containers/`+id+`/archive`, checkArchive),
	rt("DELETE", `/containers/`+id, checkManagedOrHelper),

	rt("POST", `/exec/`+id+`/start`, checkExecOwner),
	rt("POST", `/exec/`+id+`/resize`, checkExecOwner),
	rt("GET", `/exec/`+id+`/json`, checkExecOwner),
}

// maxBody bounds request bodies the proxy reads to validate. Archive uploads (the
// resolver configuration) are small; nothing Syntaxis sends is larger.
const maxBody = 4 << 20

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pth := versionPrefix.ReplaceAllString(r.URL.Path, "/")
	pth = "/" + strings.TrimPrefix(path.Clean(pth), "/")
	for _, rt := range routes {
		if r.Method != rt.method {
			continue
		}
		m := rt.re.FindStringSubmatch(pth)
		if m == nil {
			continue
		}
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
			if err != nil {
				http.Error(w, "docker proxy: could not read the request", http.StatusBadRequest)
				return
			}
			if len(b) > maxBody {
				p.refuse(w, r, errors.New("request body too large"))
				return
			}
			body = b
		}
		if rt.check != nil {
			nb, err := rt.check(p, r, m, body)
			if err != nil {
				p.refuse(w, r, err)
				return
			}
			if nb != nil {
				body = nb
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		if len(body) == 0 {
			r.Body = http.NoBody
		}
		p.rp.ServeHTTP(w, r)
		return
	}
	p.refuse(w, r, errors.New("endpoint not allowed"))
}

func (p *Proxy) refuse(w http.ResponseWriter, r *http.Request, err error) {
	p.cfg.Log.Warn("docker proxy refused a request", "method", r.Method, "path", r.URL.Path, "reason", err.Error())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": RefusalMarker + ": " + err.Error()})
}

// ProbePath is a request every strict proxy refuses and a permissive one passes on to
// the daemon (which answers 404). The health check uses it to tell them apart.
const ProbePath = "/containers/syntaxis-proxy-probe/export"

// RefusalMarker appears in every refusal.
const RefusalMarker = "refused by the Syntaxis Docker proxy"

// ---- upstream lookups ----

func (p *Proxy) getJSON(ctx context.Context, pth string, v any) error {
	// fixed host (the daemon socket); pth is built here from ids that matched the route patterns
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+pth, nil) //nolint:gosec
	if err != nil {
		return err
	}
	resp, err := p.hc.Do(req) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", pth, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

type inspected struct {
	Config struct {
		Labels map[string]string
	}
	State struct {
		Running bool
	}
	HostConfig struct {
		Runtime string
	}
	Mounts []struct {
		Name string
	}
}

func (p *Proxy) inspect(ctx context.Context, cid string) (*inspected, error) {
	var c inspected
	if err := p.getJSON(ctx, "/containers/"+url.PathEscape(cid)+"/json", &c); err != nil {
		return nil, fmt.Errorf("unknown container: %w", err)
	}
	return &c, nil
}

// ---- checks ----

func decodeStrict(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("empty request body")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func checkNetworkCreate(p *Proxy, _ *http.Request, _ []string, body []byte) ([]byte, error) {
	var b network.CreateRequest
	if err := decodeStrict(body, &b); err != nil {
		return nil, err
	}
	if b.Name != p.cfg.Network {
		return nil, fmt.Errorf("only the sandbox network %q may be created", p.cfg.Network)
	}
	if b.Driver != "" && b.Driver != "bridge" {
		return nil, errors.New("only bridge networks may be created")
	}
	if b.ConfigFrom != nil || b.ConfigOnly || b.Ingress || b.Scope != "" && b.Scope != "local" {
		return nil, errors.New("network options not allowed")
	}
	for k := range b.Options {
		if k != "com.docker.network.bridge.enable_icc" {
			return nil, fmt.Errorf("network option %q not allowed", k)
		}
	}
	return json.Marshal(b)
}

func checkVolumeCreate(_ *Proxy, _ *http.Request, _ []string, body []byte) ([]byte, error) {
	var b volume.CreateOptions
	if err := decodeStrict(body, &b); err != nil {
		return nil, err
	}
	if !volumeName.MatchString(b.Name) {
		return nil, fmt.Errorf("volume name %q is not a Syntaxis volume", b.Name)
	}
	if (b.Driver != "" && b.Driver != "local") || len(b.DriverOpts) > 0 || b.ClusterVolumeSpec != nil {
		// local driver options can bind arbitrary host paths (type=none,o=bind,device=/)
		return nil, errors.New("volume driver options not allowed")
	}
	return json.Marshal(b)
}

func checkVolumeName(_ *Proxy, _ *http.Request, m []string, _ []byte) ([]byte, error) {
	if !volumeName.MatchString(m[1]) {
		return nil, fmt.Errorf("volume %q is not a Syntaxis volume", m[1])
	}
	return nil, nil
}

// checkContainerList makes a container listing show only Syntaxis containers, whatever filters
// were asked for, so a compromised server cannot learn what else runs on the host.
func checkContainerList(_ *Proxy, r *http.Request, _ []string, _ []byte) ([]byte, error) {
	q := r.URL.Query()
	filters := map[string][]string{}
	if raw := q.Get("filters"); raw != "" {
		// the API accepts {"label":["a=b"]} and the older {"label":{"a=b":true}}
		var generic map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &generic); err != nil {
			return nil, errors.New("bad filters")
		}
		for k, v := range generic {
			var list []string
			var set map[string]bool
			if json.Unmarshal(v, &list) == nil {
				filters[k] = list
			} else if json.Unmarshal(v, &set) == nil {
				for s := range set {
					filters[k] = append(filters[k], s)
				}
			} else {
				return nil, errors.New("bad filters")
			}
		}
	}
	have := false
	for _, l := range filters["label"] {
		if l == LabelManaged+"=true" {
			have = true
		}
	}
	if !have {
		filters["label"] = append(filters["label"], LabelManaged+"=true")
	}
	b, _ := json.Marshal(filters)
	q.Set("filters", string(b))
	r.URL.RawQuery = q.Encode()
	return nil, nil
}

func checkManaged(p *Proxy, r *http.Request, m []string, _ []byte) ([]byte, error) {
	c, err := p.inspect(r.Context(), m[1])
	if err != nil {
		return nil, err
	}
	if c.Config.Labels[LabelManaged] != "true" {
		return nil, errors.New("not a Syntaxis sandbox")
	}
	return nil, nil
}

func checkManagedOrHelper(p *Proxy, r *http.Request, m []string, _ []byte) ([]byte, error) {
	c, err := p.inspect(r.Context(), m[1])
	if err != nil {
		return nil, err
	}
	if c.Config.Labels[LabelManaged] != "true" && c.Config.Labels[LabelHelper] != "true" {
		return nil, errors.New("not a Syntaxis container")
	}
	return nil, nil
}

// checkStart refuses to start anything that is not a gVisor sandbox, which also keeps
// the never-started volume helper from ever running.
func checkStart(p *Proxy, r *http.Request, m []string, _ []byte) ([]byte, error) {
	c, err := p.inspect(r.Context(), m[1])
	if err != nil {
		return nil, err
	}
	if c.Config.Labels[LabelManaged] != "true" || !strings.HasPrefix(c.HostConfig.Runtime, "runsc") {
		return nil, errors.New("only gVisor sandboxes may be started")
	}
	return nil, nil
}

// checkArchive allows copying files only into a stopped helper whose only mount is the
// resolver configuration volume.
func checkArchive(p *Proxy, r *http.Request, m []string, _ []byte) ([]byte, error) {
	c, err := p.inspect(r.Context(), m[1])
	if err != nil {
		return nil, err
	}
	if c.Config.Labels[LabelHelper] != "true" || c.State.Running {
		return nil, errors.New("files may only be copied into the stopped volume helper")
	}
	if r.URL.Query().Get("path") != "/vol" {
		return nil, errors.New("files may only be copied to /vol")
	}
	for _, mt := range c.Mounts {
		if mt.Name != DNSVolume {
			return nil, errors.New("the helper may only mount the resolver volume")
		}
	}
	return nil, nil
}

func checkUpdate(p *Proxy, r *http.Request, m []string, body []byte) ([]byte, error) {
	if _, err := checkManaged(p, r, m, nil); err != nil {
		return nil, err
	}
	var b container.UpdateConfig
	if err := decodeStrict(body, &b); err != nil {
		return nil, err
	}
	if len(b.Devices) > 0 || len(b.DeviceRequests) > 0 || len(b.DeviceCgroupRules) > 0 || b.CgroupParent != "" {
		return nil, errors.New("only resource limits may be updated")
	}
	if b.Memory < 0 || b.NanoCPUs < 0 || (b.PidsLimit != nil && *b.PidsLimit <= 0) {
		return nil, errors.New("limits may not be removed")
	}
	return json.Marshal(b)
}

func isRoot(user string) bool {
	u, _, _ := strings.Cut(user, ":")
	return u == "root" || u == "0"
}

func checkExecCreate(p *Proxy, r *http.Request, m []string, body []byte) ([]byte, error) {
	if _, err := checkManaged(p, r, m, nil); err != nil {
		return nil, err
	}
	var b container.ExecOptions
	if err := decodeStrict(body, &b); err != nil {
		return nil, err
	}
	if b.Privileged {
		return nil, errors.New("privileged exec not allowed")
	}
	if isRoot(b.User) {
		return nil, errors.New("exec as root not allowed")
	}
	return json.Marshal(b)
}

func checkExecOwner(p *Proxy, r *http.Request, m []string, _ []byte) ([]byte, error) {
	var e struct{ ContainerID string }
	if err := p.getJSON(r.Context(), "/exec/"+url.PathEscape(m[1])+"/json", &e); err != nil {
		return nil, fmt.Errorf("unknown exec: %w", err)
	}
	return checkManaged(p, r, []string{"", e.ContainerID}, nil)
}

// createBody is the container create request: Config inline plus HostConfig.
type createBody struct {
	container.Config
	HostConfig       *container.HostConfig     `json:"HostConfig,omitempty"`
	NetworkingConfig *network.NetworkingConfig `json:"NetworkingConfig,omitempty"`
}

func checkContainerCreate(p *Proxy, r *http.Request, _ []string, body []byte) ([]byte, error) {
	var b createBody
	if err := decodeStrict(body, &b); err != nil {
		return nil, err
	}
	hc := b.HostConfig
	if hc == nil {
		return nil, errors.New("a host configuration is required")
	}
	helper := b.Labels[LabelHelper] == "true"
	sandbox := b.Labels[LabelManaged] == "true"
	if helper == sandbox {
		return nil, errors.New("containers must be labelled as exactly one of sandbox or helper")
	}
	var errs []string
	add := func(s string) { errs = append(errs, s) }

	if hc.Privileged {
		add("privileged is forbidden")
	}
	if len(hc.CapAdd) > 0 {
		add("adding capabilities is forbidden")
	}
	if !hasAll(hc.CapDrop) {
		add("all capabilities must be dropped")
	}
	if len(hc.Binds) > 0 || len(hc.VolumesFrom) > 0 || len(hc.Links) > 0 {
		add("binds, volumes-from and links are forbidden")
	}
	if len(hc.Devices) > 0 || len(hc.DeviceRequests) > 0 || len(hc.DeviceCgroupRules) > 0 {
		add("devices are forbidden")
	}
	if len(hc.PortBindings) > 0 || hc.PublishAllPorts || len(b.ExposedPorts) > 0 {
		add("published ports are forbidden")
	}
	if len(hc.Sysctls) > 0 || len(hc.ExtraHosts) > 0 || hc.CgroupParent != "" || len(hc.GroupAdd) > 0 {
		add("sysctls, extra hosts, cgroup parent and extra groups are forbidden")
	}
	if hc.OomKillDisable != nil && *hc.OomKillDisable {
		add("disabling the OOM killer is forbidden")
	}
	for name, mode := range map[string]string{"pid": string(hc.PidMode), "ipc": string(hc.IpcMode), "uts": string(hc.UTSMode), "userns": string(hc.UsernsMode), "cgroupns": string(hc.CgroupnsMode)} {
		if mode != "" && mode != "private" && !(name == "ipc" && (mode == "none" || mode == "shareable")) {
			add(name + " namespace mode " + mode + " is forbidden")
		}
	}
	if hc.Cgroup != "" {
		add("cgroup mode is forbidden")
	}
	if nm := string(hc.NetworkMode); nm != "none" && nm != p.cfg.Network {
		add(fmt.Sprintf("network %q is not the sandbox network", nm))
	}
	if b.NetworkingConfig != nil {
		for n := range b.NetworkingConfig.EndpointsConfig {
			if n != p.cfg.Network {
				add(fmt.Sprintf("network %q is not the sandbox network", n))
			}
		}
	}
	for _, o := range hc.SecurityOpt {
		if o != "no-new-privileges" && o != "no-new-privileges:true" {
			add("security option " + o + " is forbidden")
		}
	}
	if hc.Runtime != "" && !strings.HasPrefix(hc.Runtime, "runsc") {
		add("runtime must be gVisor (runsc)")
	}
	if sandbox {
		if !strings.HasPrefix(hc.Runtime, "runsc") {
			add("sandboxes must use gVisor (runsc)")
		}
		if !hasNNP(hc.SecurityOpt) {
			add("no-new-privileges is required")
		}
		if !hc.ReadonlyRootfs {
			add("the root filesystem must be read-only")
		}
		if b.User == "" || isRoot(b.User) {
			add("sandboxes must run as a non-root user")
		}
		if hc.Memory <= 0 || hc.NanoCPUs <= 0 || hc.PidsLimit == nil || *hc.PidsLimit <= 0 {
			add("memory, CPU and process limits are required")
		}
	}
	if helper {
		// created only to receive a file through the copy API; checkStart keeps it from running
		if hc.NetworkMode != "none" {
			add("the helper must have no network")
		}
	}
	dnsMount := ""
	for _, mt := range hc.Mounts {
		switch mt.Type {
		case mount.TypeVolume:
			if !volumeName.MatchString(mt.Source) {
				add(fmt.Sprintf("volume %q is not a Syntaxis volume", mt.Source))
			}
			if mt.VolumeOptions != nil && (mt.VolumeOptions.DriverConfig != nil || mt.VolumeOptions.Subpath != "") {
				add("volume driver options and subpaths are forbidden")
			}
			if helper && mt.Source != DNSVolume {
				add("the helper may only mount the resolver volume")
			}
		case mount.TypeTmpfs:
		case mount.TypeBind:
			// only the resolver configuration file, read-only, from the resolver volume
			// (whose contents only Syntaxis writes; workspace volumes could hold symlinks)
			if dnsMount == "" {
				var v volume.Volume
				if err := p.getJSON(r.Context(), "/volumes/"+DNSVolume, &v); err != nil {
					add("the resolver volume does not exist")
					continue
				}
				dnsMount = v.Mountpoint
			}
			if !mt.ReadOnly || mt.Source != path.Join(dnsMount, "resolv.conf") || mt.Target != "/etc/resolv.conf" || mt.BindOptions != nil && mt.BindOptions.Propagation != "" {
				add(fmt.Sprintf("bind mount of %q is forbidden", mt.Source))
			}
		default:
			add(fmt.Sprintf("mount type %q is forbidden", mt.Type))
		}
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return json.Marshal(b)
}

func hasAll(caps []string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, "ALL") {
			return true
		}
	}
	return false
}

func hasNNP(opts []string) bool {
	for _, o := range opts {
		if o == "no-new-privileges" || o == "no-new-privileges:true" {
			return true
		}
	}
	return false
}
