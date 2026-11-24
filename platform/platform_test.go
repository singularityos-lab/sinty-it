package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

type login1 struct {
	mu     sync.Mutex
	calls  []string
	people []session
}

func (l *login1) ListSessions() ([]session, *dbus.Error) {
	return append([]session{{ID: "c0", UID: 0, User: "root", Path: "/org/freedesktop/login1/session/c0"}}, l.people...), nil
}

func (l *login1) Reboot(interactive bool) *dbus.Error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "reboot")
	return nil
}

type sessionObj struct {
	l  *login1
	id string
}

func (s sessionObj) Lock() *dbus.Error {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	s.l.calls = append(s.l.calls, "lock "+s.id)
	return nil
}

func (s sessionObj) Terminate() *dbus.Error {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	s.l.calls = append(s.l.calls, "terminate "+s.id)
	return nil
}

func privateBus(t *testing.T) string {
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>unix:path=`+filepath.Join(dir, "bus")+`</listen>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>`), 0o644)
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork", "--print-address=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}

func fakeUpdated(t *testing.T, sock string, channel *string, downloads *int) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { reply(w, map[string]string{"state": "idle"}) })
	mux.HandleFunc("GET /check", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"state": "available", "latest": "26A190", "consent_token": "tok"})
	})
	mux.HandleFunc("POST /download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "tok" {
			w.WriteHeader(http.StatusConflict)
			reply(w, map[string]string{"error": "stale"})
			return
		}
		*downloads++
		reply(w, map[string]string{"state": "downloading", "latest": "26A190"})
	})
	mux.HandleFunc("GET /channel", func(w http.ResponseWriter, r *http.Request) { reply(w, map[string]string{"channel": *channel}) })
	mux.HandleFunc("POST /channel", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name != "stable" && name != "nightly" {
			w.WriteHeader(http.StatusConflict)
			reply(w, map[string]string{"error": "unknown channel"})
			return
		}
		*channel = name
		reply(w, map[string]string{"channel": name})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func TestPlatform(t *testing.T) {
	addr := privateBus(t)
	server, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	l := &login1{people: []session{
		{ID: "2", UID: 1000, User: "alice", Path: "/org/freedesktop/login1/session/_32"},
		{ID: "3", UID: 1001, User: "bob", Path: "/org/freedesktop/login1/session/_33"},
	}}
	server.Export(l, loginPath, loginManager)
	for _, s := range l.people {
		server.Export(sessionObj{l: l, id: s.ID}, s.Path, loginSession)
	}
	server.Export(sessionObj{l: l, id: "c0"}, "/org/freedesktop/login1/session/c0", loginSession)
	if r, err := server.RequestName(loginDest, 0); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("name: %v %v", r, err)
	}
	dir := t.TempDir()
	channel, downloads := "stable", 0
	fakeUpdated(t, filepath.Join(dir, "updated.sock"), &channel, &downloads)
	key := filepath.Join(dir, "sintykey")
	os.WriteFile(key, []byte("#!/bin/sh\ncase $1 in tier) echo 'tpm 6';; wipe-var) echo wiped >> "+filepath.Join(dir, "wiped")+"; echo 'sintykey: wiped 5 key artifact(s)';; esac\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "os-release"), []byte("NAME=\"Sinty OS\"\nVERSION_ID=26\nBUILD_ID=26A190\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "fw.json"), []byte(`{"enabled":true,"profile":"home","rules":[]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "model"), []byte("Test Laptop\n"), 0o644)
	p := New(Paths{OSRelease: filepath.Join(dir, "os-release"), Firewall: filepath.Join(dir, "fw.json"), UpdateSock: filepath.Join(dir, "updated.sock"),
		SintyKey: key, PolicyFile: filepath.Join(dir, "policy", "policy.json"), DMIProduct: filepath.Join(dir, "model"), Data: dir})
	p.Bus = func() (*dbus.Conn, error) { return dbus.Connect(addr) }
	f := p.Facts()
	if f.OS != "Sinty OS" || f.Version != "26 26A190" || !f.Encrypted || !f.Sealed || !f.Firewall || f.Channel != "stable" || f.Model != "Test Laptop" || f.DiskFree <= 0 || f.BootHealth != "idle" {
		t.Fatalf("facts: %+v", f)
	}
	h := p.Handlers()
	ctx := context.Background()
	if out, err := h["lock"](ctx, nil); err != nil || out != "2 session(s): alice, bob" {
		t.Fatalf("lock: %q %v", out, err)
	}
	if _, err := h["logout"](ctx, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := h["update"](ctx, nil); err != nil || out != "downloading 26A190" || downloads != 1 {
		t.Fatalf("update: %q %v %d", out, err, downloads)
	}
	if out, err := h["wipe"](ctx, nil); err != nil || !strings.Contains(out, "wiped 5") {
		t.Fatalf("wipe: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiped")); err != nil {
		t.Fatal("the wipe did not reach sintykey")
	}
	l.mu.Lock()
	got := strings.Join(l.calls, ",")
	l.mu.Unlock()
	if got != "lock 2,lock 3,terminate 2,terminate 3,reboot" {
		t.Fatalf("login1 calls: %s", got)
	}
	if err := p.ApplyPolicy(map[string]string{"updates.channel": "beta"}); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	if _, err := os.Stat(p.Paths.PolicyFile); err == nil {
		t.Fatal("a policy that failed to apply was recorded as applied")
	}
	if err := p.ApplyPolicy(map[string]string{"updates.channel": "nightly", "screen.lock-after": "300"}); err != nil || channel != "nightly" {
		t.Fatalf("policy: %v %s", err, channel)
	}
	raw, _ := os.ReadFile(p.Paths.PolicyFile)
	if !strings.Contains(string(raw), `"screen.lock-after": "300"`) {
		t.Fatalf("policy file: %s", raw)
	}
}
