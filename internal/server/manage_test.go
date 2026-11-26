package server

import (
	"context"
	"net/http"
	"testing"
)

func TestManage(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	var ran []string
	var tok struct{ Token string }
	l.call("alice:3", "POST", "/api/v1/tokens", map[string]any{"group": "office", "uses": 3, "hours": 2}, &tok)
	l.call("alice:3", "POST", "/api/v1/tokens", map[string]any{"group": "lab", "uses": 1, "hours": 1}, nil)
	var list struct {
		Tokens []tokenView `json:"tokens"`
	}
	if c := l.call("bob:3", "GET", "/api/v1/tokens", nil, &list); c != http.StatusOK || len(list.Tokens) != 2 || list.Tokens[0].Group != "lab" || list.Tokens[1].Uses != 3 {
		t.Fatalf("tokens: %d %+v", c, list.Tokens)
	}
	if c := l.call("bob:3", "DELETE", "/api/v1/tokens/"+list.Tokens[0].ID, nil, nil); c != http.StatusNoContent {
		t.Fatalf("revoke: %d", c)
	}
	if c := l.call("bob:3", "DELETE", "/api/v1/tokens/"+list.Tokens[0].ID, nil, nil); c != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", c)
	}
	if c := l.call("bob:3", "DELETE", "/api/v1/tokens/x", nil, nil); c != http.StatusNotFound {
		t.Fatalf("a short id: %d", c)
	}
	l.call("bob:3", "GET", "/api/v1/tokens", nil, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].Group != "office" {
		t.Fatalf("after revoking: %+v", list.Tokens)
	}

	pc := l.device("pc1", &ran)
	id, err := pc.Enroll(ctx, l.srv.URL, tok.Token, "Reception")
	if err != nil {
		t.Fatal(err)
	}
	l.call("bob:3", "GET", "/api/v1/tokens", nil, &list)
	if list.Tokens[0].Uses != 2 {
		t.Fatalf("an enrollment did not use the code: %+v", list.Tokens)
	}
	var v deviceView
	if c := l.call("alice:3", "PATCH", "/api/v1/devices/"+id.Device, map[string]any{"name": "  Front Desk ", "group": "lab"}, &v); c != http.StatusOK || v.Name != "Front Desk" || v.Group != "lab" {
		t.Fatalf("edit: %d %+v", c, v)
	}
	for _, bad := range []map[string]any{{"name": " "}, {"group": "Lab Room"}} {
		if c := l.call("alice:3", "PATCH", "/api/v1/devices/"+id.Device, bad, nil); c != http.StatusBadRequest {
			t.Fatalf("%v: %d", bad, c)
		}
	}
	if c := l.call("alice:3", "PATCH", "/api/v1/devices/nope", map[string]any{"name": "x"}, nil); c != http.StatusNotFound {
		t.Fatalf("unknown device: %d", c)
	}

	l.call("alice:3", "PUT", "/api/v1/policies/lab", map[string]string{"screen.lock-after": "300"}, nil)
	if c := l.call("alice:3", "DELETE", "/api/v1/policies/lab", nil, nil); c != http.StatusNoContent {
		t.Fatalf("drop policy: %d", c)
	}
	if c := l.call("alice:3", "DELETE", "/api/v1/policies/lab", nil, nil); c != http.StatusNotFound {
		t.Fatalf("drop policy twice: %d", c)
	}

	var log struct {
		Entries []struct {
			Action string            `json:"action"`
			Detail map[string]string `json:"detail"`
		} `json:"entries"`
	}
	l.call("carol:3", "GET", "/api/v1/audit", nil, &log)
	seen := map[string]bool{}
	for _, e := range log.Entries {
		seen[e.Action] = true
	}
	for _, a := range []string{"token.revoke", "device.edit", "policy.delete"} {
		if !seen[a] {
			t.Fatalf("%s is not in the log", a)
		}
	}
}
