package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-identity/rp"
	"github.com/singularityos-lab/sinty-it/audit"
	"github.com/singularityos-lab/sinty-it/command"
	"github.com/singularityos-lab/sinty-it/internal/state"
	"github.com/singularityos-lab/sinty-it/wire"
)

//go:embed app.html
var appHTML []byte

//go:embed app.js
var appJS []byte

type Config struct {
	Admins      []string
	MinLevel    int
	CommandTTL  time.Duration
	ClockSkew   time.Duration
	CommandKey  ed25519.PrivateKey
	UI          string
	Secure      bool
	MaxPerBatch int
}

type Server struct {
	cfg   Config
	Store *state.Store
	Audit *audit.Log
	Web   *rp.Web
	Now   func() time.Time
}

var (
	errNotAdmin = errors.New("sinty-it: not an administrator")
	groupName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	settingKey  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)
)

func New(cfg Config, auth rp.Authenticator, store *state.Store, log *audit.Log) *Server {
	if cfg.MinLevel == 0 {
		cfg.MinLevel = 3
	}
	if cfg.CommandTTL == 0 {
		cfg.CommandTTL = 24 * time.Hour
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 5 * time.Minute
	}
	if cfg.MaxPerBatch == 0 {
		cfg.MaxPerBatch = 16
	}
	s := &Server{cfg: cfg, Store: store, Audit: log, Now: time.Now}
	s.Web = rp.NewWeb(auth, "sinty_it", cfg.Secure, func(c rp.Claims) (string, error) {
		if !slices.Contains(cfg.Admins, c.Subject) {
			return "", errNotAdmin
		}
		return c.Subject, nil
	})
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.Web.Login)
	mux.HandleFunc("GET /cb", s.Web.Callback)
	mux.HandleFunc("POST /logout", s.Web.Logout)
	mux.HandleFunc("GET /{$}", s.app)
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(appJS)
	})
	mux.HandleFunc("POST /agent/v1/enroll", s.enroll)
	mux.HandleFunc("POST /agent/v1/checkin", s.checkin)
	mux.HandleFunc("GET /api/v1/me", s.admin(s.me))
	mux.HandleFunc("GET /api/v1/devices", s.admin(s.devices))
	mux.HandleFunc("GET /api/v1/devices/{id}", s.admin(s.device))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.admin(s.retire))
	mux.HandleFunc("POST /api/v1/devices/{id}/commands", s.admin(s.send))
	mux.HandleFunc("GET /api/v1/commands", s.admin(s.commands))
	mux.HandleFunc("POST /api/v1/commands/{id}/approve", s.admin(s.approve))
	mux.HandleFunc("POST /api/v1/commands/{id}/cancel", s.admin(s.cancel))
	mux.HandleFunc("POST /api/v1/tokens", s.admin(s.newToken))
	mux.HandleFunc("GET /api/v1/policies", s.admin(s.policies))
	mux.HandleFunc("PUT /api/v1/policies/{group}", s.admin(s.setPolicy))
	mux.HandleFunc("GET /api/v1/audit", s.admin(s.audit))
	if s.cfg.UI != "" {
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir(s.cfg.UI))))
	}
	return rp.SecureHeaders(mux)
}

func (s *Server) app(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.Web.Page(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(appHTML)
}

type adminHandler func(w http.ResponseWriter, r *http.Request, who string)

func (s *Server) admin(h adminHandler) http.HandlerFunc {
	return s.Web.API(func(w http.ResponseWriter, r *http.Request, id rp.Identity) {
		if id.Claims.Level < s.cfg.MinLevel {
			rp.StepUp(w, id.Claims.Level, s.cfg.MinLevel)
			return
		}
		h(w, r, id.User)
	})
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func (s *Server) record(actor, action, target string, detail map[string]string) error {
	_, err := s.Audit.Append(actor, action, target, detail)
	return err
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in wire.Enroll
	if !decode(w, r, 1<<14, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Key) != ed25519.PublicKeySize || in.Name == "" || len(in.Name) > 64 {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	now := s.Now()
	var dev *state.Device
	err := s.Store.Update(func(st *state.State) error {
		h := hashToken(in.Token)
		i := slices.IndexFunc(st.Tokens, func(t state.Token) bool { return t.Hash == h })
		if i < 0 || !now.Before(st.Tokens[i].Expires) || st.Tokens[i].Uses <= 0 {
			return errBadToken
		}
		st.Tokens[i].Uses--
		group := st.Tokens[i].Group
		if st.Tokens[i].Uses == 0 {
			st.Tokens = slices.Delete(st.Tokens, i, i+1)
		}
		for _, d := range st.Devices {
			if bytes.Equal(d.Key, in.Key) {
				return errBadToken
			}
		}
		dev = &state.Device{ID: random(12), Name: in.Name, Group: group, Key: in.Key, Enrolled: now, Seen: now, Facts: in.Facts}
		st.Devices[dev.ID] = dev
		return nil
	})
	if errors.Is(err, errBadToken) {
		rp.Fail(w, http.StatusForbidden, "invalid_token")
		return
	}
	if err != nil {
		rp.Fail(w, http.StatusInternalServerError, "server_error")
		return
	}
	s.record("device:"+dev.ID, "device.enroll", dev.ID, map[string]string{"name": dev.Name, "group": dev.Group})
	rp.Reply(w, wire.Enrolled{Device: dev.ID, Group: dev.Group, ServerKey: s.cfg.CommandKey.Public().(ed25519.PublicKey)})
}

var errBadToken = errors.New("sinty-it: enrollment token refused")

func (s *Server) checkin(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(wire.SignatureHeader))
	var in wire.Checkin
	if err != nil || json.Unmarshal(body, &in) != nil {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	now := s.Now()
	var out wire.Orders
	var finished []*state.Command
	err = s.Store.Update(func(st *state.State) error {
		d := st.Devices[in.Device]
		if d == nil || d.Retired || !ed25519.Verify(ed25519.PublicKey(d.Key), wire.CheckinMessage(body), sig) {
			return errDevice
		}
		at := time.UnixMilli(in.Clock)
		if in.Clock <= d.Clock || at.Sub(now).Abs() > s.cfg.ClockSkew {
			return errClock
		}
		d.Clock, d.Seen, d.Facts = in.Clock, now, in.Facts
		for _, res := range in.Results {
			c := st.Commands[res.ID]
			if c == nil || c.Device != d.ID || c.State != state.Sent {
				continue
			}
			c.State, c.Output, c.Finished = state.Failed, truncate(res.Output, 4096), now
			if res.OK {
				c.State = state.Done
			}
			finished = append(finished, c)
		}
		var mine []*state.Command
		for _, c := range st.Commands {
			if c.Device != d.ID {
				continue
			}
			if (c.State == state.Queued || c.State == state.Sent || c.State == state.Waiting) && !now.Before(c.Expires) {
				c.State, c.Finished = state.Expired, now
				continue
			}
			if c.State == state.Queued || c.State == state.Sent {
				mine = append(mine, c)
			}
		}
		sort.Slice(mine, func(i, j int) bool {
			if (mine[i].Seq == 0) != (mine[j].Seq == 0) {
				return mine[i].Seq != 0
			}
			if mine[i].Seq != mine[j].Seq {
				return mine[i].Seq < mine[j].Seq
			}
			return mine[i].Created.Before(mine[j].Created)
		})
		if len(mine) > s.cfg.MaxPerBatch {
			mine = mine[:s.cfg.MaxPerBatch]
		}
		for _, c := range mine {
			if c.State == state.Queued {
				d.Seq++
				c.Seq, c.State = d.Seq, state.Sent
			}
			signed, err := command.Sign(s.cfg.CommandKey, command.Command{ID: c.ID, Device: d.ID, Seq: c.Seq, Kind: c.Kind, Args: c.Args, Issued: c.Created, Expires: c.Expires})
			if err != nil {
				return err
			}
			out.Commands = append(out.Commands, signed)
		}
		if p := st.Policies[d.Group]; p != nil {
			out.Policy = p.Settings
		}
		return nil
	})
	switch {
	case errors.Is(err, errDevice):
		rp.Fail(w, http.StatusUnauthorized, "unknown_device")
		return
	case errors.Is(err, errClock):
		rp.Fail(w, http.StatusConflict, "clock")
		return
	case err != nil:
		rp.Fail(w, http.StatusInternalServerError, "server_error")
		return
	}
	for _, c := range finished {
		s.record("device:"+c.Device, "command."+c.State, c.Device, map[string]string{"command": c.ID, "kind": c.Kind})
	}
	rp.Reply(w, out)
}

var (
	errDevice = errors.New("sinty-it: unknown device")
	errClock  = errors.New("sinty-it: stale or skewed clock")
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, who string) {
	rp.Reply(w, map[string]any{"user": who})
}

type deviceView struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Group    string     `json:"group"`
	Enrolled time.Time  `json:"enrolled"`
	Seen     time.Time  `json:"seen"`
	Facts    wire.Facts `json:"facts"`
	Retired  bool       `json:"retired,omitempty"`
	Issues   []string   `json:"issues"`
}

func (s *Server) view(d *state.Device) deviceView {
	v := deviceView{ID: d.ID, Name: d.Name, Group: d.Group, Enrolled: d.Enrolled, Seen: d.Seen, Facts: d.Facts, Retired: d.Retired, Issues: []string{}}
	if !d.Facts.Encrypted {
		v.Issues = append(v.Issues, "not_encrypted")
	}
	if !d.Facts.Sealed {
		v.Issues = append(v.Issues, "not_sealed")
	}
	if !d.Facts.Firewall {
		v.Issues = append(v.Issues, "firewall_off")
	}
	if s.Now().Sub(d.Seen) > 7*24*time.Hour {
		v.Issues = append(v.Issues, "not_seen")
	}
	return v
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request, who string) {
	list := []deviceView{}
	s.Store.View(func(st *state.State) {
		for _, d := range st.Devices {
			if !d.Retired {
				list = append(list, s.view(d))
			}
		}
	})
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	rp.Reply(w, map[string]any{"devices": list})
}

func (s *Server) device(w http.ResponseWriter, r *http.Request, who string) {
	var v deviceView
	cmds := []*state.Command{}
	found := false
	s.Store.View(func(st *state.State) {
		d := st.Devices[r.PathValue("id")]
		if d == nil {
			return
		}
		found, v = true, s.view(d)
		for _, c := range st.Commands {
			if c.Device == d.ID {
				cp := *c
				cmds = append(cmds, &cp)
			}
		}
	})
	if !found {
		rp.Fail(w, http.StatusNotFound, "not_found")
		return
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Created.After(cmds[j].Created) })
	rp.Reply(w, map[string]any{"device": v, "commands": cmds})
}

func (s *Server) retire(w http.ResponseWriter, r *http.Request, who string) {
	id := r.PathValue("id")
	err := s.Store.Update(func(st *state.State) error {
		d := st.Devices[id]
		if d == nil || d.Retired {
			return errNotFound
		}
		d.Retired = true
		for _, c := range st.Commands {
			if c.Device == id && (c.State == state.Queued || c.State == state.Sent || c.State == state.Waiting) {
				c.State, c.Finished = state.Canceled, s.Now()
			}
		}
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "device.retire", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

var (
	errNotFound = errors.New("sinty-it: not found")
	errKind     = errors.New("sinty-it: unknown command")
	errSelf     = errors.New("sinty-it: another administrator must approve")
	errDone     = errors.New("sinty-it: the command is not waiting")
)

func (s *Server) fail(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		rp.Fail(w, http.StatusNotFound, "not_found")
	case errors.Is(err, errKind):
		rp.Fail(w, http.StatusBadRequest, "unknown_command")
	case errors.Is(err, errSelf):
		rp.Fail(w, http.StatusForbidden, "second_admin")
	case errors.Is(err, errDone):
		rp.Fail(w, http.StatusConflict, "not_waiting")
	default:
		rp.Fail(w, http.StatusInternalServerError, "server_error")
	}
	return true
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, who string) {
	var in struct {
		Kind string            `json:"kind"`
		Args map[string]string `json:"args"`
	}
	if !decode(w, r, 1<<14, &in) {
		return
	}
	id := r.PathValue("id")
	now := s.Now()
	c := &state.Command{ID: random(12), Device: id, Kind: in.Kind, Args: in.Args, By: who, Created: now, Expires: now.Add(s.cfg.CommandTTL), State: state.Queued}
	if command.Dangerous[in.Kind] {
		c.State = state.Waiting
	}
	err := s.Store.Update(func(st *state.State) error {
		if !command.Known[in.Kind] {
			return errKind
		}
		if d := st.Devices[id]; d == nil || d.Retired {
			return errNotFound
		}
		st.Commands[c.ID] = c
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "command.create", id, map[string]string{"command": c.ID, "kind": c.Kind, "state": c.State})
	w.WriteHeader(http.StatusCreated)
	rp.Reply(w, c)
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request, who string) {
	want := r.URL.Query().Get("state")
	list := []state.Command{}
	s.Store.View(func(st *state.State) {
		for _, c := range st.Commands {
			if want == "" || c.State == want {
				list = append(list, *c)
			}
		}
	})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	rp.Reply(w, map[string]any{"commands": list})
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request, who string) {
	id := r.PathValue("id")
	var c state.Command
	err := s.Store.Update(func(st *state.State) error {
		cmd := st.Commands[id]
		if cmd == nil {
			return errNotFound
		}
		if cmd.State != state.Waiting || !s.Now().Before(cmd.Expires) {
			return errDone
		}
		if cmd.By == who || slices.Contains(cmd.Approvers, who) {
			return errSelf
		}
		cmd.Approvers = append(cmd.Approvers, who)
		cmd.State = state.Queued
		c = *cmd
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "command.approve", c.Device, map[string]string{"command": c.ID, "kind": c.Kind})
	rp.Reply(w, c)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, who string) {
	id := r.PathValue("id")
	var c state.Command
	err := s.Store.Update(func(st *state.State) error {
		cmd := st.Commands[id]
		if cmd == nil {
			return errNotFound
		}
		if cmd.State != state.Waiting && cmd.State != state.Queued {
			return errDone
		}
		cmd.State, cmd.Finished = state.Canceled, s.Now()
		c = *cmd
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "command.cancel", c.Device, map[string]string{"command": c.ID, "kind": c.Kind})
	rp.Reply(w, c)
}

func (s *Server) newToken(w http.ResponseWriter, r *http.Request, who string) {
	var in struct {
		Group string `json:"group"`
		Uses  int    `json:"uses"`
		Hours int    `json:"hours"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	if in.Group == "" {
		in.Group = "default"
	}
	if !groupName.MatchString(in.Group) || in.Uses < 1 || in.Uses > 1000 || in.Hours < 1 || in.Hours > 24*30 {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	t := "sit_" + random(24)
	exp := s.Now().Add(time.Duration(in.Hours) * time.Hour)
	err := s.Store.Update(func(st *state.State) error {
		st.Tokens = append(st.Tokens, state.Token{Hash: hashToken(t), Group: in.Group, By: who, Uses: in.Uses, Expires: exp})
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "token.create", in.Group, map[string]string{"uses": fmt.Sprint(in.Uses), "expires": exp.UTC().Format(time.RFC3339)})
	w.WriteHeader(http.StatusCreated)
	rp.Reply(w, map[string]any{"token": t, "group": in.Group, "uses": in.Uses, "expires": exp})
}

func (s *Server) policies(w http.ResponseWriter, r *http.Request, who string) {
	out := map[string]state.Policy{}
	s.Store.View(func(st *state.State) {
		for g, p := range st.Policies {
			out[g] = *p
		}
	})
	rp.Reply(w, map[string]any{"policies": out})
}

func (s *Server) setPolicy(w http.ResponseWriter, r *http.Request, who string) {
	group := r.PathValue("group")
	var in map[string]string
	if !decode(w, r, 1<<16, &in) {
		return
	}
	if !groupName.MatchString(group) || len(in) > 256 {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	for k, v := range in {
		if !settingKey.MatchString(k) || len(v) > 1024 {
			rp.Fail(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	err := s.Store.Update(func(st *state.State) error {
		st.Policies[group] = &state.Policy{Settings: in, Updated: s.Now(), By: who}
		return nil
	})
	if s.fail(w, err) {
		return
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s.record(who, "policy.set", group, map[string]string{"keys": strings.Join(keys, ",")})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, who string) {
	entries := []audit.Entry{}
	err := s.Audit.Each(func(e audit.Entry) { entries = append(entries, e) })
	if err != nil {
		rp.Fail(w, http.StatusInternalServerError, "audit_broken")
		return
	}
	if len(entries) > 500 {
		entries = entries[len(entries)-500:]
	}
	slices.Reverse(entries)
	names := map[string]string{}
	s.Store.View(func(st *state.State) {
		for id, d := range st.Devices {
			names[id] = d.Name
		}
	})
	rp.Reply(w, map[string]any{"entries": entries, "verified": true, "devices": names})
}
