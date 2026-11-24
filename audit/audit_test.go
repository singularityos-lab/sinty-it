package audit

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChain(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"token.create", "device.enroll", "command.send"} {
		if _, err := l.Append("alice", a, "pc1", map[string]string{"k": "v"}); err != nil {
			t.Fatal(err)
		}
	}
	again, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := again.Append("bob", "command.approve", "pc1", nil); e.Seq != 4 {
		t.Fatalf("the chain did not continue after reopening: %d", e.Seq)
	}
	n := 0
	if err := Verify(path, pub, func(Entry) { n++ }); err != nil || n != 4 {
		t.Fatalf("verify: %v %d", err, n)
	}
	raw, _ := os.ReadFile(path)
	tampered := bytes.Replace(raw, []byte(`"actor":"bob"`), []byte(`"actor":"eve"`), 1)
	if err := VerifyReader(bytes.NewReader(tampered), pub, nil); !errors.Is(err, ErrBroken) {
		t.Fatal("an edited entry was not noticed")
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	dropped := bytes.Join(append(lines[:1:1], lines[2:]...), []byte("\n"))
	if err := VerifyReader(bytes.NewReader(dropped), pub, nil); !errors.Is(err, ErrBroken) {
		t.Fatal("a removed entry was not noticed")
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if err := Verify(path, other, nil); !errors.Is(err, ErrBroken) {
		t.Fatal("the log verified with another key")
	}
}
