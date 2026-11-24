package audit

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Entry struct {
	Seq    int64             `json:"seq"`
	Time   time.Time         `json:"time"`
	Actor  string            `json:"actor"`
	Action string            `json:"action"`
	Target string            `json:"target,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
	Prev   string            `json:"prev"`
	Hash   string            `json:"hash"`
	Sig    []byte            `json:"sig"`
}

type Log struct {
	path string
	key  ed25519.PrivateKey
	mu   sync.Mutex
	seq  int64
	last string
}

var ErrBroken = errors.New("audit: the log has been altered")

func digest(e Entry) string {
	e.Hash, e.Sig = "", nil
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func Open(path string, key ed25519.PrivateKey) (*Log, error) {
	l := &Log{path: path, key: key, last: genesis}
	err := Verify(path, key.Public().(ed25519.PublicKey), func(e Entry) {
		l.seq, l.last = e.Seq, e.Hash
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return l, nil
}

const genesis = "0000000000000000000000000000000000000000000000000000000000000000"

func (l *Log) Append(actor, action, target string, detail map[string]string) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{Seq: l.seq + 1, Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Detail: detail, Prev: l.last}
	e.Hash = digest(e)
	e.Sig = ed25519.Sign(l.key, []byte(e.Hash))
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return e, err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return e, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return e, err
	}
	if err := f.Close(); err != nil {
		return e, err
	}
	l.seq, l.last = e.Seq, e.Hash
	return e, nil
}

func Verify(path string, pub ed25519.PublicKey, each func(Entry)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return VerifyReader(f, pub, each)
}

func VerifyReader(r io.Reader, pub ed25519.PublicKey, each func(Entry)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	prev, seq := genesis, int64(0)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fmt.Errorf("%w: entry %d is not readable", ErrBroken, seq+1)
		}
		if e.Seq != seq+1 || e.Prev != prev || digest(e) != e.Hash || !ed25519.Verify(pub, []byte(e.Hash), e.Sig) {
			return fmt.Errorf("%w at entry %d", ErrBroken, seq+1)
		}
		if each != nil {
			each(e)
		}
		prev, seq = e.Hash, e.Seq
	}
	return sc.Err()
}

func (l *Log) Each(each func(Entry)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := Verify(l.path, l.key.Public().(ed25519.PublicKey), each)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
