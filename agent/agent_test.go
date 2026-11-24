package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/singularityos-lab/sinty-it/command"
)

func TestRun(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	ran := 0
	dir := t.TempDir()
	a := &Agent{Dir: dir, Now: func() time.Time { return now },
		Handlers: map[string]Handler{"lock": func(context.Context, map[string]string) (string, error) { ran++; return "locked", nil }}}
	if _, err := a.Run(context.Background(), command.Signed{}); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal("a device that never enrolled ran a command")
	}
	writeFile(dir+"/identity.json", Identity{Server: "x", Device: "pc1", Key: other, ServerKey: pub})
	cmd := func(id string, seq int64, dev, kind string, exp time.Duration, k ed25519.PrivateKey) command.Signed {
		s, _ := command.Sign(k, command.Command{ID: id, Device: dev, Seq: seq, Kind: kind, Issued: now, Expires: now.Add(exp)})
		return s
	}
	ctx := context.Background()
	first := cmd("c1", 1, "pc1", "lock", time.Hour, key)
	if r, err := a.Run(ctx, first); err != nil || !r.OK || r.Output != "locked" {
		t.Fatalf("a valid command did not run: %v %+v", err, r)
	}
	if r, err := a.Run(ctx, first); err != nil || r.Output != "locked" || ran != 1 {
		t.Fatalf("a command sent again ran twice or lost its result: %v %+v %d", err, r, ran)
	}
	if _, err := a.Run(ctx, cmd("c2", 1, "pc1", "lock", time.Hour, key)); !errors.Is(err, ErrReplay) {
		t.Fatal("a command with an old number ran")
	}
	if _, err := a.Run(ctx, cmd("c3", 2, "pc2", "lock", time.Hour, key)); !errors.Is(err, ErrOtherDevice) {
		t.Fatal("a command for another device ran")
	}
	if _, err := a.Run(ctx, cmd("c4", 3, "pc1", "lock", -time.Second, key)); !errors.Is(err, ErrExpired) {
		t.Fatal("an expired command ran")
	}
	if _, err := a.Run(ctx, cmd("c5", 4, "pc1", "lock", time.Hour, other)); !errors.Is(err, command.ErrSignature) {
		t.Fatal("a command signed with another key ran")
	}
	if r, err := a.Run(ctx, cmd("c6", 5, "pc1", "wipe", time.Hour, key)); err != nil || r.OK {
		t.Fatal("a command without a handler did not fail")
	}
	s := cmd("c7", 6, "pc1", "lock", time.Hour, key)
	s.Payload = append(s.Payload[:len(s.Payload)-1], []byte(`,"x":1}`)...)
	if _, err := a.Run(ctx, s); !errors.Is(err, command.ErrSignature) {
		t.Fatal("an edited command ran")
	}
	restarted := &Agent{Dir: dir, Now: a.Now, Handlers: a.Handlers}
	if _, err := restarted.Run(ctx, cmd("c8", 2, "pc1", "lock", time.Hour, key)); !errors.Is(err, ErrReplay) {
		t.Fatal("a restart forgot the commands already run")
	}
	if ran != 1 {
		t.Fatalf("handler ran %d times", ran)
	}
}
