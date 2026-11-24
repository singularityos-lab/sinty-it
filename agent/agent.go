package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/singularityos-lab/sinty-it/command"
	"github.com/singularityos-lab/sinty-it/wire"
)

var (
	ErrOtherDevice = errors.New("agent: the command is for another device")
	ErrReplay      = errors.New("agent: the command was already run or is out of order")
	ErrExpired     = errors.New("agent: the command has expired")
	ErrUnknown     = errors.New("agent: this device does not run that command")
	ErrNotEnrolled = errors.New("agent: this device is not enrolled")
	ErrRefused     = errors.New("agent: the server refused the device")
)

type Handler func(ctx context.Context, args map[string]string) (string, error)

type Identity struct {
	Server    string             `json:"server"`
	Device    string             `json:"device"`
	Group     string             `json:"group"`
	Key       ed25519.PrivateKey `json:"key"`
	ServerKey ed25519.PublicKey  `json:"server_key"`
}

type memory struct {
	Seq     int64                  `json:"seq"`
	Clock   int64                  `json:"clock"`
	Results map[string]wire.Result `json:"results"`
	Order   []string               `json:"order"`
	Pending []wire.Result          `json:"pending"`
}

type Agent struct {
	Dir      string
	Handlers map[string]Handler
	Facts    func() wire.Facts
	Policy   func(map[string]string) error
	HTTP     *http.Client
	Now      func() time.Time

	mu  sync.Mutex
	id  *Identity
	mem memory
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func writeFile(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *Agent) load() error {
	if a.id != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(a.Dir, "identity.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotEnrolled
	}
	if err != nil {
		return err
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return err
	}
	a.id = &id
	if raw, err := os.ReadFile(filepath.Join(a.Dir, "memory.json")); err == nil {
		json.Unmarshal(raw, &a.mem)
	}
	if a.mem.Results == nil {
		a.mem.Results = map[string]wire.Result{}
	}
	return nil
}

func (a *Agent) Enrolled() (Identity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(); err != nil {
		return Identity{}, err
	}
	return *a.id, nil
}

func post(ctx context.Context, c *http.Client, url string, body []byte, header map[string]string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("server answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, json.Unmarshal(raw, out)
}

func (a *Agent) Enroll(ctx context.Context, server, token, name string) (Identity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(); err == nil {
		return Identity{}, errors.New("agent: this device is already enrolled")
	}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Identity{}, err
	}
	var facts wire.Facts
	if a.Facts != nil {
		facts = a.Facts()
	}
	server = strings.TrimRight(server, "/")
	body, _ := json.Marshal(wire.Enroll{Token: token, Name: name, Key: pub, Facts: facts})
	var out wire.Enrolled
	if code, err := post(ctx, a.client(), server+"/agent/v1/enroll", body, nil, &out); err != nil {
		if code == http.StatusForbidden {
			return Identity{}, fmt.Errorf("%w: %v", ErrRefused, err)
		}
		return Identity{}, err
	}
	if len(out.ServerKey) != ed25519.PublicKeySize || out.Device == "" {
		return Identity{}, errors.New("agent: the server sent an invalid enrollment")
	}
	id := Identity{Server: server, Device: out.Device, Group: out.Group, Key: key, ServerKey: out.ServerKey}
	if err := writeFile(filepath.Join(a.Dir, "identity.json"), id); err != nil {
		return Identity{}, err
	}
	a.id, a.mem = &id, memory{Results: map[string]wire.Result{}}
	return id, nil
}

func (a *Agent) remember(r wire.Result) {
	a.mem.Results[r.ID] = r
	a.mem.Order = append(a.mem.Order, r.ID)
	for len(a.mem.Order) > 64 {
		delete(a.mem.Results, a.mem.Order[0])
		a.mem.Order = a.mem.Order[1:]
	}
	a.mem.Pending = append(a.mem.Pending, r)
}

func (a *Agent) run(ctx context.Context, s command.Signed) (wire.Result, error) {
	c, err := command.Open(a.id.ServerKey, s)
	if err != nil {
		return wire.Result{}, err
	}
	if old, ok := a.mem.Results[c.ID]; ok && c.Seq <= a.mem.Seq {
		if !slices.ContainsFunc(a.mem.Pending, func(r wire.Result) bool { return r.ID == old.ID }) {
			a.mem.Pending = append(a.mem.Pending, old)
		}
		return old, nil
	}
	switch {
	case c.Device != a.id.Device:
		return wire.Result{}, ErrOtherDevice
	case c.Seq <= a.mem.Seq:
		return wire.Result{}, ErrReplay
	case !a.now().Before(c.Expires):
		return wire.Result{}, ErrExpired
	}
	a.mem.Seq = c.Seq
	h := a.Handlers[c.Kind]
	var r wire.Result
	if h == nil {
		r = wire.Result{ID: c.ID, OK: false, Output: ErrUnknown.Error() + ": " + c.Kind}
	} else {
		out, err := h(ctx, c.Args)
		r = wire.Result{ID: c.ID, OK: err == nil, Output: out}
		if err != nil {
			r.Output = strings.TrimSpace(out + "\n" + err.Error())
		}
	}
	a.remember(r)
	return r, writeFile(filepath.Join(a.Dir, "memory.json"), a.mem)
}

func (a *Agent) Run(ctx context.Context, s command.Signed) (wire.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(); err != nil {
		return wire.Result{}, err
	}
	return a.run(ctx, s)
}

func (a *Agent) Checkin(ctx context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.load(); err != nil {
		return 0, err
	}
	clock := max(a.now().UnixMilli(), a.mem.Clock+1)
	var facts wire.Facts
	if a.Facts != nil {
		facts = a.Facts()
	}
	body, _ := json.Marshal(wire.Checkin{Device: a.id.Device, Clock: clock, Facts: facts, Results: a.mem.Pending})
	sig := ed25519.Sign(a.id.Key, wire.CheckinMessage(body))
	a.mem.Clock = clock
	var out wire.Orders
	code, err := post(ctx, a.client(), a.id.Server+"/agent/v1/checkin", body, map[string]string{wire.SignatureHeader: base64.StdEncoding.EncodeToString(sig)}, &out)
	if err != nil {
		writeFile(filepath.Join(a.Dir, "memory.json"), a.mem)
		if code == http.StatusUnauthorized {
			return 0, fmt.Errorf("%w: %v", ErrRefused, err)
		}
		return 0, err
	}
	a.mem.Pending = nil
	if a.Policy != nil && out.Policy != nil {
		if err := a.Policy(out.Policy); err != nil {
			return 0, err
		}
	}
	ran := 0
	for _, s := range out.Commands {
		r, err := a.run(ctx, s)
		if err != nil {
			continue
		}
		if r.ID != "" {
			ran++
		}
	}
	return ran, writeFile(filepath.Join(a.Dir, "memory.json"), a.mem)
}

func (a *Agent) Loop(ctx context.Context, every time.Duration, logf func(string, ...any)) error {
	for {
		n, err := a.Checkin(ctx)
		if errors.Is(err, ErrRefused) || errors.Is(err, ErrNotEnrolled) {
			return err
		}
		if err != nil {
			logf("check-in failed: %v", err)
		} else if n > 0 {
			logf("ran %d command(s)", n)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}
