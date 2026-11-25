package wire

import (
	"github.com/singularityos-lab/sinty-it/command"
)

const SignatureHeader = "Sinty-Device-Signature"

type Facts struct {
	Hostname   string `json:"hostname,omitempty"`
	OS         string `json:"os,omitempty"`
	Version    string `json:"version,omitempty"`
	Arch       string `json:"arch,omitempty"`
	Model      string `json:"model,omitempty"`
	Encrypted  bool   `json:"encrypted"`
	Sealed     bool   `json:"sealed"`
	Firewall   bool   `json:"firewall"`
	Channel    string `json:"channel,omitempty"`
	DiskFree   int64  `json:"disk_free,omitempty"`
	LastUser   string `json:"last_user,omitempty"`
	BootHealth string `json:"boot_health,omitempty"`
}

type Enroll struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Key   []byte `json:"key"`
	Facts Facts  `json:"facts"`
}

type Enrolled struct {
	Device    string `json:"device"`
	Group     string `json:"group"`
	ServerKey []byte `json:"server_key"`
}

type Result struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

type Checkin struct {
	Device  string   `json:"device"`
	Clock   int64    `json:"clock"`
	Facts   Facts    `json:"facts"`
	Results []Result `json:"results,omitempty"`
	Leaving bool     `json:"leaving,omitempty"`
}

type Orders struct {
	Commands []command.Signed  `json:"commands"`
	Policy   map[string]string `json:"policy,omitempty"`
}

func CheckinMessage(body []byte) []byte {
	return append([]byte("sinty-it checkin v1\n"), body...)
}
