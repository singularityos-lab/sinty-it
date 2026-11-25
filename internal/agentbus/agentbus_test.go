package agentbus

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/singularityos-lab/sinty-it/agent"
	"github.com/singularityos-lab/sinty-it/wire"
)

func TestServer(t *testing.T) {
	for in, want := range map[string][2]string{
		"it.example.org":                            {"https://it.example.org", ""},
		" https://it.example.org/ ":                 {"https://it.example.org", ""},
		"https://it.example.org/enroll?token=sit_a": {"https://it.example.org", "sit_a"},
		"https://example.org/it/enroll#sit_b":       {"https://example.org/it", "sit_b"},
	} {
		addr, tok, err := Server(in)
		if err != nil || addr != want[0] || tok != want[1] {
			t.Errorf("%q: %q %q %v", in, addr, tok, err)
		}
	}
	for _, bad := range []string{"http://it.example.org", "", "https://"} {
		if _, _, err := Server(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEnrollAndLeave(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	var got wire.Enroll
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/enroll" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		if got.Token != "sit_good" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"refused"}`))
			return
		}
		json.NewEncoder(w).Encode(wire.Enrolled{Device: "d1", Group: "office", ServerKey: pub})
	}))
	defer srv.Close()
	allow := map[string]bool{}
	started, stopped, changed := 0, 0, 0
	s := &Service{
		Agent: &agent.Agent{Dir: t.TempDir(), HTTP: srv.Client()},
		Check: func(_ dbus.Sender, action string) *dbus.Error {
			if allow[action] {
				return nil
			}
			return dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"no"})
		},
		Start: func(context.Context) error { started++; return nil },
		Stop:  func(context.Context) error { stopped++; return nil },
		Emit:  func(string, ...any) { changed++ },
	}
	if on, _, _, _, err := s.Status(); err != nil || on {
		t.Fatal("a new device reports enrolled")
	}
	if _, _, err := s.Enroll(":1.5", srv.URL, "sit_good", "Laptop"); err == nil || err.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Fatalf("enrolled without authorization: %v", err)
	}
	if got.Token != "" {
		t.Fatal("the server was contacted before authorization")
	}
	allow[ActionEnroll] = true
	if _, _, err := s.Enroll(":1.5", srv.URL, "sit_bad", "Laptop"); err == nil || err.Name != Interface+".Error.Refused" {
		t.Fatalf("a refused code: %v", err)
	}
	if _, _, err := s.Enroll(":1.5", srv.URL, "", " "); err == nil || err.Name != Interface+".Error.Invalid" {
		t.Fatalf("empty fields: %v", err)
	}
	dev, group, err := s.Enroll(":1.5", srv.URL+"/enroll?token=sit_good", "", "Laptop")
	if err != nil || dev != "d1" || group != "office" || got.Name != "Laptop" {
		t.Fatalf("enroll: %v %q %q %+v", err, dev, group, got)
	}
	if started != 1 || changed != 1 {
		t.Fatalf("started %d changed %d", started, changed)
	}
	on, server, device, grp, e := s.Status()
	if e != nil || !on || server != srv.URL || device != "d1" || grp != "office" {
		t.Fatalf("status: %v %v %q %q %q", e, on, server, device, grp)
	}
	if _, _, err := s.Enroll(":1.5", srv.URL, "sit_good", "Laptop"); err == nil {
		t.Fatal("enrolled twice")
	}
	if err := s.Leave(":1.5"); err == nil {
		t.Fatal("left without authorization")
	}
	allow[ActionLeave] = true
	if err := s.Leave(":1.5"); err != nil || stopped != 1 {
		t.Fatalf("leave: %v %d", err, stopped)
	}
	if on, _, _, _, _ := s.Status(); on {
		t.Fatal("still enrolled after leaving")
	}
	if err := s.Leave(":1.5"); err != nil || stopped != 1 {
		t.Fatal("leaving twice did something")
	}
	if _, _, err := s.Enroll(":1.5", srv.URL, "sit_good", "Laptop"); err != nil {
		t.Fatalf("enroll again after leaving: %v", err)
	}
}
