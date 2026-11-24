package state

import (
	"encoding/json"
	"errors"
	"github.com/singularityos-lab/sinty-it/wire"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Device struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Group    string     `json:"group"`
	Key      []byte     `json:"key"`
	Enrolled time.Time  `json:"enrolled"`
	Seen     time.Time  `json:"seen"`
	Clock    int64      `json:"clock"`
	Facts    wire.Facts `json:"facts"`
	Seq      int64      `json:"seq"`
	Retired  bool       `json:"retired,omitempty"`
}

type Command struct {
	ID        string            `json:"id"`
	Device    string            `json:"device"`
	Kind      string            `json:"kind"`
	Args      map[string]string `json:"args,omitempty"`
	By        string            `json:"by"`
	Approvers []string          `json:"approvers,omitempty"`
	Created   time.Time         `json:"created"`
	Expires   time.Time         `json:"expires"`
	State     string            `json:"state"`
	Seq       int64             `json:"seq,omitempty"`
	Output    string            `json:"output,omitempty"`
	Finished  time.Time         `json:"finished,omitempty"`
}

const (
	Waiting  = "waiting_approval"
	Queued   = "queued"
	Sent     = "sent"
	Done     = "done"
	Failed   = "failed"
	Expired  = "expired"
	Canceled = "canceled"
)

type Token struct {
	Hash    string    `json:"hash"`
	Group   string    `json:"group"`
	By      string    `json:"by"`
	Uses    int       `json:"uses"`
	Expires time.Time `json:"expires"`
}

type Policy struct {
	Settings map[string]string `json:"settings"`
	Updated  time.Time         `json:"updated"`
	By       string            `json:"by"`
}

type State struct {
	Devices  map[string]*Device  `json:"devices"`
	Commands map[string]*Command `json:"commands"`
	Tokens   []Token             `json:"tokens"`
	Policies map[string]*Policy  `json:"policies"`
}

type Store struct {
	path string
	mu   sync.Mutex
	st   State
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, st: State{Devices: map[string]*Device{}, Commands: map[string]*Command{}, Policies: map[string]*Policy{}}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, err
	}
	if s.st.Devices == nil {
		s.st.Devices = map[string]*Device{}
	}
	if s.st.Commands == nil {
		s.st.Commands = map[string]*Command{}
	}
	if s.st.Policies == nil {
		s.st.Policies = map[string]*Policy{}
	}
	return s, nil
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

func (s *Store) View(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
}

func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.st)
	if err := fn(&s.st); err != nil {
		var back State
		json.Unmarshal(raw, &back)
		s.st = back
		return err
	}
	return s.save()
}
