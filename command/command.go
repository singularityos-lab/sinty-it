package command

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"
)

type Command struct {
	ID      string            `json:"id"`
	Device  string            `json:"device"`
	Seq     int64             `json:"seq"`
	Kind    string            `json:"kind"`
	Args    map[string]string `json:"args,omitempty"`
	Issued  time.Time         `json:"issued"`
	Expires time.Time         `json:"expires"`
}

type Signed struct {
	Payload []byte `json:"payload"`
	Sig     []byte `json:"sig"`
}

var ErrSignature = errors.New("command: not signed by the management key")

func Sign(key ed25519.PrivateKey, c Command) (Signed, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return Signed{}, err
	}
	return Signed{Payload: raw, Sig: ed25519.Sign(key, append([]byte("sinty-it command v1\n"), raw...))}, nil
}

func Open(pub ed25519.PublicKey, s Signed) (Command, error) {
	var c Command
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, append([]byte("sinty-it command v1\n"), s.Payload...), s.Sig) {
		return c, ErrSignature
	}
	if err := json.Unmarshal(s.Payload, &c); err != nil {
		return c, ErrSignature
	}
	return c, nil
}

var Dangerous = map[string]bool{"wipe": true}

var Known = map[string]bool{"sync-policy": true, "lock": true, "logout": true, "update": true, "reboot": true, "wipe": true}
