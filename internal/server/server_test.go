package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-identity/rp"
	"github.com/singularityos-lab/sinty-it/agent"
	"github.com/singularityos-lab/sinty-it/audit"
	"github.com/singularityos-lab/sinty-it/internal/state"
	"github.com/singularityos-lab/sinty-it/wire"
)

type fakeAuth struct{}

func (fakeAuth) Begin(...string) rp.Request {
	return rp.Request{URL: "https://id.example/", State: "s"}
}

func (fakeAuth) Finish(context.Context, rp.Request, url.Values) (rp.Claims, error) {
	return rp.Claims{}, rp.ErrState
}

func (fakeAuth) Access(_ context.Context, bearer string) (rp.Claims, error) {
	who, level, ok := strings.Cut(bearer, ":")
	if !ok {
		return rp.Claims{}, rp.ErrToken
	}
	return rp.Claims{Subject: who, Level: int(level[0] - '0')}, nil
}

type lab struct {
	t   *testing.T
	srv *httptest.Server
	s   *Server
	dir string
	pub ed25519.PublicKey
}

func newLab(t *testing.T) *lab {
	dir := t.TempDir()
	_, cmdKey, _ := ed25519.GenerateKey(nil)
	pub, auditKey, _ := ed25519.GenerateKey(nil)
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.log"), auditKey)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Admins: []string{"alice", "bob", "carol"}, CommandKey: cmdKey}, fakeAuth{}, st, log)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &lab{t: t, srv: srv, s: s, dir: dir, pub: pub}
}

func (l *lab) call(who, method, path string, body any, out any) int {
	var r *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	} else {
		r = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, l.srv.URL+path, r)
	if who != "" {
		req.Header.Set("Authorization", "Bearer "+who)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (l *lab) device(name string, ran *[]string) *agent.Agent {
	return &agent.Agent{Dir: filepath.Join(l.dir, name),
		Facts: func() wire.Facts {
			return wire.Facts{Hostname: name, OS: "Sinty", Encrypted: true, Sealed: name != "old", Firewall: true}
		},
		Handlers: map[string]agent.Handler{
			"lock": func(context.Context, map[string]string) (string, error) {
				*ran = append(*ran, name+":lock")
				return "locked", nil
			},
			"wipe": func(context.Context, map[string]string) (string, error) {
				*ran = append(*ran, name+":wipe")
				return "wiped", nil
			},
			"update": func(context.Context, map[string]string) (string, error) {
				return "no space", errors.New("exit status 1")
			},
		}}
}

func TestFleet(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	var ran []string
	if c := l.call("", "GET", "/api/v1/devices", nil, nil); c != http.StatusBadRequest {
		t.Fatalf("an anonymous call got %d", c)
	}
	if c := l.call("mallory:3", "GET", "/api/v1/devices", nil, nil); c != http.StatusForbidden {
		t.Fatalf("someone who is not an administrator got %d", c)
	}
	if c := l.call("alice:2", "GET", "/api/v1/devices", nil, nil); c != http.StatusForbidden {
		t.Fatalf("an administrator without a device sign-in got %d", c)
	}
	weak, _ := http.NewRequest("GET", l.srv.URL+"/api/v1/devices", nil)
	weak.Header.Set("Authorization", "Bearer alice:2")
	if resp, err := http.DefaultClient.Do(weak); err != nil || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `acr_values="sinty:3"`) {
		t.Fatal("a weak sign-in was not told which level to reach")
	} else {
		resp.Body.Close()
	}
	var tok struct{ Token string }
	if c := l.call("alice:3", "POST", "/api/v1/tokens", map[string]any{"group": "office", "uses": 2, "hours": 1}, &tok); c != http.StatusCreated || !strings.HasPrefix(tok.Token, "sit_") {
		t.Fatalf("token: %d %q", c, tok.Token)
	}
	pc1, pc2, old := l.device("pc1", &ran), l.device("pc2", &ran), l.device("old", &ran)
	if _, err := pc1.Enroll(ctx, l.srv.URL, "sit_wrong", "Reception"); !errors.Is(err, agent.ErrRefused) {
		t.Fatalf("a wrong token enrolled a device: %v", err)
	}
	id1, err := pc1.Enroll(ctx, l.srv.URL, tok.Token, "Reception")
	if err != nil || id1.Group != "office" {
		t.Fatalf("enroll: %v %+v", err, id1)
	}
	if _, err := pc2.Enroll(ctx, l.srv.URL, tok.Token, "Studio"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Enroll(ctx, l.srv.URL, tok.Token, "Old laptop"); !errors.Is(err, agent.ErrRefused) {
		t.Fatal("a token was used more times than allowed")
	}
	if c := l.call("alice:3", "PUT", "/api/v1/policies/office", map[string]string{"updates.channel": "stable", "screen.lock-after": "300"}, nil); c != http.StatusNoContent {
		t.Fatalf("policy %d", c)
	}
	if c := l.call("alice:3", "PUT", "/api/v1/policies/office", map[string]string{"Bad Key": "x"}, nil); c != http.StatusBadRequest {
		t.Fatal("a malformed policy key was accepted")
	}
	var got map[string]string
	pc1.Policy = func(p map[string]string) error { got = p; return nil }
	if _, err := pc1.Checkin(ctx); err != nil || got["updates.channel"] != "stable" {
		t.Fatalf("the policy did not reach the device: %v %v", err, got)
	}
	var lock state.Command
	if c := l.call("bob:3", "POST", "/api/v1/devices/"+id1.Device+"/commands", map[string]any{"kind": "lock"}, &lock); c != http.StatusCreated || lock.State != state.Queued {
		t.Fatalf("lock: %d %+v", c, lock)
	}
	if c := l.call("bob:3", "POST", "/api/v1/devices/"+id1.Device+"/commands", map[string]any{"kind": "format-c"}, nil); c != http.StatusBadRequest {
		t.Fatal("an unknown command was queued")
	}
	var upd state.Command
	l.call("bob:3", "POST", "/api/v1/devices/"+id1.Device+"/commands", map[string]any{"kind": "update"}, &upd)
	if n, err := pc2.Checkin(ctx); err != nil || n != 0 {
		t.Fatal("a command for pc1 reached pc2")
	}
	if n, err := pc1.Checkin(ctx); err != nil || n != 2 {
		t.Fatalf("pc1 ran %d commands: %v", n, err)
	}
	pc1.Checkin(ctx)
	var dv struct {
		Device   deviceView
		Commands []state.Command
	}
	l.call("alice:3", "GET", "/api/v1/devices/"+id1.Device, nil, &dv)
	states := map[string]string{}
	for _, c := range dv.Commands {
		states[c.Kind] = c.State + ":" + c.Output
	}
	if states["lock"] != "done:locked" || !strings.HasPrefix(states["update"], "failed:no space") {
		t.Fatalf("results did not come back: %v", states)
	}
	var wipe state.Command
	l.call("alice:3", "POST", "/api/v1/devices/"+id1.Device+"/commands", map[string]any{"kind": "wipe"}, &wipe)
	if wipe.State != state.Waiting {
		t.Fatalf("a wipe was queued without a second administrator: %s", wipe.State)
	}
	if n, _ := pc1.Checkin(ctx); n != 0 {
		t.Fatal("a wipe ran before approval")
	}
	if c := l.call("alice:3", "POST", "/api/v1/commands/"+wipe.ID+"/approve", nil, nil); c != http.StatusForbidden {
		t.Fatal("an administrator approved their own wipe")
	}
	if c := l.call("carol:3", "POST", "/api/v1/commands/"+wipe.ID+"/approve", nil, nil); c != http.StatusOK {
		t.Fatalf("approve %d", c)
	}
	if c := l.call("bob:3", "POST", "/api/v1/commands/"+wipe.ID+"/approve", nil, nil); c != http.StatusConflict {
		t.Fatal("an approved wipe was approved again")
	}
	if n, _ := pc1.Checkin(ctx); n != 1 || ran[len(ran)-1] != "pc1:wipe" {
		t.Fatalf("the approved wipe did not run: %v", ran)
	}
	var last int64
	l.s.Store.View(func(st *state.State) { last = st.Devices[id1.Device].Clock })
	if c := l.raw(id1.Device, id1.Key, wire.Checkin{Device: id1.Device, Clock: last}); c != http.StatusConflict {
		t.Fatalf("a check-in replaying the last clock got %d", c)
	}
	if c := l.raw(id1.Device, id1.Key, wire.Checkin{Device: id1.Device, Clock: 1}); c != http.StatusConflict {
		t.Fatalf("a check-in with a clock far in the past got %d", c)
	}
	var lock2 state.Command
	l.call("bob:3", "POST", "/api/v1/devices/"+id1.Device+"/commands", map[string]any{"kind": "lock"}, &lock2)
	pc1.Handlers["lock"] = func(context.Context, map[string]string) (string, error) { return "", errors.New("offline") }
	l.s.Store.Update(func(st *state.State) error { st.Commands[lock2.ID].State = state.Sent; return nil })
	id2, _ := pc2.Enrolled()
	var clock2 int64
	l.s.Store.View(func(st *state.State) { clock2 = st.Devices[id2.Device].Clock })
	if c := l.raw(id2.Device, id2.Key, wire.Checkin{Device: id2.Device, Clock: max(clock2+1, time.Now().UnixMilli()), Results: []wire.Result{{ID: lock2.ID, OK: true, Output: "fake"}}}); c != http.StatusOK {
		t.Fatalf("pc2 check-in got %d", c)
	}
	l.s.Store.View(func(st *state.State) {
		if st.Commands[lock2.ID].State != state.Sent {
			t.Fatal("a device reported the result of another device's command")
		}
	})
	l.call("bob:3", "POST", "/api/v1/commands/"+lock2.ID+"/cancel", nil, nil)
	_, stranger, _ := ed25519.GenerateKey(nil)
	forgedID, _ := pc2.Enrolled()
	forgedID.Key = stranger
	forgedDir := filepath.Join(l.dir, "forged")
	writeJSON(t, filepath.Join(forgedDir, "identity.json"), forgedID)
	if _, err := (&agent.Agent{Dir: forgedDir}).Checkin(ctx); !errors.Is(err, agent.ErrRefused) {
		t.Fatalf("a check-in signed by another key was accepted: %v", err)
	}
	var list struct{ Devices []deviceView }
	l.call("alice:3", "GET", "/api/v1/devices", nil, &list)
	if len(list.Devices) != 2 {
		t.Fatalf("inventory has %d devices", len(list.Devices))
	}
	if c := l.call("alice:3", "DELETE", "/api/v1/devices/"+id1.Device, nil, nil); c != http.StatusNoContent {
		t.Fatal(c)
	}
	if _, err := pc1.Checkin(ctx); !errors.Is(err, agent.ErrRefused) {
		t.Fatal("a retired device still checked in")
	}
	var log struct {
		Entries  []audit.Entry
		Verified bool
	}
	l.call("alice:3", "GET", "/api/v1/audit", nil, &log)
	actions := []string{}
	for _, e := range log.Entries {
		actions = append(actions, e.Actor+" "+e.Action)
	}
	joined := strings.Join(actions, "|")
	for _, want := range []string{"alice token.create", "alice policy.set", "bob command.create", "alice command.create", "carol command.approve", "alice device.retire", "command.done", "command.failed", "device.enroll"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the audit log misses %q: %s", want, joined)
		}
	}
	if err := audit.Verify(filepath.Join(l.dir, "audit.log"), l.pub, nil); err != nil {
		t.Fatal(err)
	}
}

func (l *lab) raw(device string, key ed25519.PrivateKey, in wire.Checkin) int {
	body, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", l.srv.URL+"/agent/v1/checkin", bytes.NewReader(body))
	req.Header.Set(wire.SignatureHeader, base64.StdEncoding.EncodeToString(ed25519.Sign(key, wire.CheckinMessage(body))))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func writeJSON(t *testing.T, path string, v any) {
	raw, _ := json.Marshal(v)
	if err := osWrite(path, raw); err != nil {
		t.Fatal(err)
	}
}
