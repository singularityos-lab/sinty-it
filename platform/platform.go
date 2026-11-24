package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/singularityos-lab/sinty-it/agent"
	"github.com/singularityos-lab/sinty-it/wire"
)

type Paths struct {
	OSRelease   string `json:"os_release"`
	Firewall    string `json:"firewall"`
	UpdateSock  string `json:"update_socket"`
	SintyKey    string `json:"sintykey"`
	PolicyFile  string `json:"policy_file"`
	DMIProduct  string `json:"dmi_product"`
	DeviceModel string `json:"device_model"`
	Data        string `json:"data"`
}

func Load(path string) (Paths, error) {
	p := Sinty()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

func Sinty() Paths {
	return Paths{
		OSRelease:   "/etc/os-release",
		Firewall:    "/var/lib/sinty-firewalld/state.json",
		UpdateSock:  "/run/updated.sock",
		SintyKey:    "/usr/bin/sintykey",
		PolicyFile:  "/var/lib/sinty-it/policy.json",
		DMIProduct:  "/sys/class/dmi/id/product_name",
		DeviceModel: "/proc/device-tree/model",
		Data:        "/var",
	}
}

type Platform struct {
	Paths Paths
	Bus   func() (*dbus.Conn, error)
	Run   func(ctx context.Context, name string, args ...string) (string, error)
}

func New(p Paths) *Platform {
	return &Platform{Paths: p, Bus: func() (*dbus.Conn, error) { return dbus.ConnectSystemBus() }, Run: run}
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func readKV(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out
}

func firstLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(strings.SplitN(string(raw), "\n", 2)[0], "\x00"))
}

func (p *Platform) updated(ctx context.Context, method, path string, out any) error {
	c := &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", p.Paths.UpdateSock)
	}}}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil {
		json.Unmarshal(raw, out)
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	return nil
}

type updateStatus struct {
	State          string `json:"state"`
	Latest         string `json:"latest"`
	ProductVersion string `json:"product_version"`
	ConsentToken   string `json:"consent_token"`
	Error          string `json:"error"`
}

func (p *Platform) Facts() wire.Facts {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	osr := readKV(p.Paths.OSRelease)
	f := wire.Facts{OS: osr["NAME"], Version: osr["VERSION_ID"], Arch: runtime.GOARCH}
	if v := osr["BUILD_ID"]; v != "" {
		f.Version = strings.TrimSpace(f.Version + " " + v)
	}
	f.Hostname, _ = os.Hostname()
	if f.Model = firstLine(p.Paths.DMIProduct); f.Model == "" {
		f.Model = firstLine(p.Paths.DeviceModel)
	}
	if raw, err := os.ReadFile(p.Paths.Firewall); err == nil {
		var fw struct {
			Enabled bool `json:"enabled"`
		}
		f.Firewall = json.Unmarshal(raw, &fw) == nil && fw.Enabled
	}
	if out, err := p.Run(ctx, p.Paths.SintyKey, "tier"); err == nil {
		tier := strings.Fields(out)
		if len(tier) > 0 {
			f.Encrypted = true
			f.Sealed = tier[0] == "tpm"
		}
	}
	var ch struct {
		Channel string `json:"channel"`
	}
	if p.updated(ctx, "GET", "/channel", &ch) == nil {
		f.Channel = ch.Channel
	}
	var st updateStatus
	if p.updated(ctx, "GET", "/status", &st) == nil {
		f.BootHealth = st.State
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(p.Paths.Data, &fs) == nil {
		f.DiskFree = int64(fs.Bavail) * int64(fs.Bsize)
	}
	return f
}

const (
	loginDest    = "org.freedesktop.login1"
	loginPath    = "/org/freedesktop/login1"
	loginManager = "org.freedesktop.login1.Manager"
	loginSession = "org.freedesktop.login1.Session"
)

type session struct {
	ID   string
	UID  uint32
	User string
	Seat string
	Path dbus.ObjectPath
}

func (p *Platform) sessions(conn *dbus.Conn) ([]session, error) {
	var list []session
	if err := conn.Object(loginDest, loginPath).Call(loginManager+".ListSessions", 0).Store(&list); err != nil {
		return nil, err
	}
	people := list[:0]
	for _, s := range list {
		if s.UID >= 1000 {
			people = append(people, s)
		}
	}
	return people, nil
}

func (p *Platform) eachSession(method string) (string, error) {
	conn, err := p.Bus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	list, err := p.sessions(conn)
	if err != nil {
		return "", err
	}
	var done []string
	var errs []error
	for _, s := range list {
		if err := conn.Object(loginDest, s.Path).Call(loginSession+"."+method, 0).Err; err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.User, err))
			continue
		}
		done = append(done, s.User)
	}
	return fmt.Sprintf("%d session(s): %s", len(done), strings.Join(done, ", ")), errors.Join(errs...)
}

func (p *Platform) reboot() (string, error) {
	conn, err := p.Bus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return "rebooting", conn.Object(loginDest, loginPath).Call(loginManager+".Reboot", 0, false).Err
}

func (p *Platform) update(ctx context.Context, _ map[string]string) (string, error) {
	var st updateStatus
	if err := p.updated(ctx, "GET", "/check", &st); err != nil {
		return "", err
	}
	if st.State != "available" && st.State != "ready" {
		return "state " + st.State + " " + st.Error, nil
	}
	if st.State == "ready" {
		return "ready " + st.Latest + ", applies at the next restart", nil
	}
	if st.ConsentToken == "" {
		return "", errors.New("the update service offered no download")
	}
	if err := p.updated(ctx, "POST", "/download?token="+url.QueryEscape(st.ConsentToken), &st); err != nil {
		return "", err
	}
	return "downloading " + st.Latest, nil
}

func (p *Platform) setChannel(ctx context.Context, name string) error {
	return p.updated(ctx, "POST", "/channel?name="+url.QueryEscape(name), nil)
}

func (p *Platform) wipe(ctx context.Context, _ map[string]string) (string, error) {
	out, err := p.Run(ctx, p.Paths.SintyKey, "wipe-var")
	if err != nil {
		return out, err
	}
	msg, err := p.reboot()
	return out + "\n" + msg, err
}

func (p *Platform) Handlers() map[string]agent.Handler {
	return map[string]agent.Handler{
		"lock":   func(context.Context, map[string]string) (string, error) { return p.eachSession("Lock") },
		"logout": func(context.Context, map[string]string) (string, error) { return p.eachSession("Terminate") },
		"reboot": func(context.Context, map[string]string) (string, error) { return p.reboot() },
		"update": p.update,
		"wipe":   p.wipe,
		"sync-policy": func(context.Context, map[string]string) (string, error) {
			return "the policy is sent with every check-in", nil
		},
	}
}

func (p *Platform) ApplyPolicy(settings map[string]string) error {
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(p.Paths.PolicyFile); err == nil && string(old) == string(raw) {
		return nil
	}
	if ch := settings["updates.channel"]; ch != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := p.setChannel(ctx, ch); err != nil {
			return fmt.Errorf("update channel %s: %w", ch, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.Paths.PolicyFile), 0o755); err != nil {
		return err
	}
	tmp := p.Paths.PolicyFile + ".new"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.Paths.PolicyFile); err != nil {
		return err
	}
	return nil
}
