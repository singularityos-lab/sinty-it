package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-identity/rp"
	"github.com/singularityos-lab/sinty-it/agent"
	"github.com/singularityos-lab/sinty-it/audit"
	"github.com/singularityos-lab/sinty-it/internal/server"
	"github.com/singularityos-lab/sinty-it/internal/state"
	"github.com/singularityos-lab/sinty-it/platform"
)

const app = "sinty-it"

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  sinty-it serve -issuer URL -client ID -secret-file FILE -public URL -admins USER[,USER] [-cli-client ID] [-state DIR] [-min-level N] [-listen ADDR] [-ui DIR] [-cert FILE -key FILE]
  sinty-it verify-audit [-state DIR]
  sinty-it login -server URL -issuer URL [-client ID]
  sinty-it logout
  sinty-it devices
  sinty-it device ID
  sinty-it run ID KIND [KEY=VALUE...]
  sinty-it commands [STATE]
  sinty-it approve COMMAND
  sinty-it cancel COMMAND
  sinty-it retire ID
  sinty-it token [-group NAME] [-uses N] [-hours N]
  sinty-it policy [GROUP [KEY=VALUE...]]
  sinty-it audit
  sinty-it agent enroll -server URL -token TOKEN [-name NAME] [-dir DIR] [-config FILE]
  sinty-it agent run [-every DURATION] [-dir DIR] [-config FILE]
  sinty-it agent status [-dir DIR]
Commands: lock, logout, reboot, update, sync-policy, wipe (a wipe waits for a second administrator).
Exit status: 0 done, 1 failed, 2 wrong usage, 3 signed out, 4 not found, 5 refused.
`)
	os.Exit(2)
}

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		err = serve(args)
	case "verify-audit":
		err = verifyAudit(args)
	case "login":
		err = login(args)
	case "logout":
		err = rp.ForgetSession(app)
	case "agent":
		err = agentCmd(args)
	case "devices", "device", "run", "commands", "approve", "cancel", "retire", "token", "policy", "audit":
		err = admin(os.Args[1], args)
	default:
		usage()
	}
	var ee exitError
	switch {
	case errors.As(err, &ee):
		fmt.Fprintln(os.Stderr, "sinty-it:", ee.msg)
		os.Exit(ee.code)
	case err != nil:
		fmt.Fprintln(os.Stderr, "sinty-it:", err)
		os.Exit(1)
	}
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key.Seed(), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a key", path)
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	issuer := fs.String("issuer", "", "identity provider")
	client := fs.String("client", "it", "client id at the identity provider")
	cliClient := fs.String("cli-client", "", "client id of the command line, whose tokens are accepted too")
	secretFile := fs.String("secret-file", "", "file holding the client secret")
	public := fs.String("public", "", "public address of this service")
	dir := fs.String("state", "/var/lib/sinty-it", "state directory")
	admins := fs.String("admins", "", "administrators, comma separated")
	minLevel := fs.Int("min-level", 3, "weakest sign-in accepted for administration")
	listen := fs.String("listen", ":8445", "address to listen on")
	ui := fs.String("ui", "/usr/share/sintyui", "sintyui directory")
	cert := fs.String("cert", "", "TLS certificate")
	key := fs.String("key", "", "TLS key")
	fs.Parse(args)
	if *issuer == "" || *public == "" || *admins == "" {
		return exitError{2, "-issuer, -public and -admins are required"}
	}
	secret := ""
	if *secretFile != "" {
		raw, err := os.ReadFile(*secretFile)
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(string(raw))
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	cmdKey, err := loadKey(filepath.Join(*dir, "command.key"))
	if err != nil {
		return err
	}
	auditKey, err := loadKey(filepath.Join(*dir, "audit.key"))
	if err != nil {
		return err
	}
	st, err := state.Open(filepath.Join(*dir, "state.json"))
	if err != nil {
		return err
	}
	trail, err := audit.Open(filepath.Join(*dir, "audit.log"), auditKey)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg := rp.Config{Issuer: *issuer, ClientID: *client, ClientSecret: secret, RedirectURL: strings.TrimRight(*public, "/") + "/cb", MinLevel: *minLevel}
	if *cliClient != "" {
		cfg.Audiences = []string{*cliClient}
	}
	auth, err := rp.Connect(ctx, cfg, func(f string, a ...any) { log.Printf(f, a...) })
	if err != nil {
		return err
	}
	var list []string
	for _, a := range strings.Split(*admins, ",") {
		if a = strings.TrimSpace(a); a != "" {
			list = append(list, a)
		}
	}
	s := server.New(server.Config{Admins: list, MinLevel: *minLevel, CommandKey: cmdKey, UI: *ui, Secure: strings.HasPrefix(*public, "https://")}, auth, st, trail)
	return rp.Serve(ctx, *listen, s.Handler(), *cert, *key)
}

func verifyAudit(args []string) error {
	fs := flag.NewFlagSet("verify-audit", flag.ExitOnError)
	dir := fs.String("state", "/var/lib/sinty-it", "state directory")
	fs.Parse(args)
	key, err := loadKey(filepath.Join(*dir, "audit.key"))
	if err != nil {
		return err
	}
	n := 0
	if err := audit.Verify(filepath.Join(*dir, "audit.log"), key.Public().(ed25519.PublicKey), func(audit.Entry) { n++ }); err != nil {
		return exitError{5, err.Error()}
	}
	fmt.Printf("%d entries, chain and signatures intact\n", n)
	return nil
}

func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	srv := fs.String("server", "", "sinty-it address")
	issuer := fs.String("issuer", "", "identity provider")
	client := fs.String("client", "it-cli", "client id of the command line")
	fs.Parse(args)
	if *srv == "" || *issuer == "" {
		return exitError{2, "-server and -issuer are required"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	claims, sess, err := rp.CommandLineLogin(ctx, app, *srv, rp.Config{Issuer: *issuer, ClientID: *client}, "sinty:3")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Signed in as %s until %s\n", claims.Name, sess.Expires.Local().Format(time.Kitchen))
	return nil
}

func mapErr(err error) error {
	var api *rp.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, rp.ErrSignedOut):
		return exitError{3, "not signed in: run sinty-it login"}
	case errors.Is(err, rp.ErrNotFound):
		return exitError{4, "not found"}
	case errors.As(err, &api) && api.Status == http.StatusForbidden:
		switch api.Code {
		case "second_admin":
			return exitError{5, "another administrator must approve this"}
		case "stronger":
			return exitError{5, "this needs a sign-in confirmed on your device: run sinty-it login"}
		}
		return exitError{5, api.Error()}
	case errors.As(err, &api) && api.Status == http.StatusConflict:
		return exitError{5, "the command is no longer waiting"}
	case errors.As(err, &api) && api.Code == "unknown_command":
		return exitError{2, "unknown command: use lock, logout, reboot, update, sync-policy or wipe"}
	case errors.As(err, &api) && api.Code == "invalid_request":
		return exitError{2, "the server refused the request as malformed"}
	}
	return err
}

func pairs(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, exitError{2, "expected KEY=VALUE, got " + a}
		}
		out[k] = v
	}
	return out, nil
}

func admin(verb string, args []string) error {
	sess, err := rp.LoadSession(app)
	if err != nil {
		return mapErr(err)
	}
	ctx := context.Background()
	need := func(n int) {
		if len(args) < n {
			usage()
		}
	}
	var out any
	switch verb {
	case "devices":
		err = sess.JSON(ctx, "GET", "/api/v1/devices", nil, &out)
	case "device":
		need(1)
		err = sess.JSON(ctx, "GET", "/api/v1/devices/"+url.PathEscape(args[0]), nil, &out)
	case "run":
		need(2)
		kv, perr := pairs(args[2:])
		if perr != nil {
			return perr
		}
		err = sess.JSON(ctx, "POST", "/api/v1/devices/"+url.PathEscape(args[0])+"/commands", map[string]any{"kind": args[1], "args": kv}, &out)
	case "commands":
		q := ""
		if len(args) > 0 {
			q = "?state=" + url.QueryEscape(args[0])
		}
		err = sess.JSON(ctx, "GET", "/api/v1/commands"+q, nil, &out)
	case "approve", "cancel":
		need(1)
		err = sess.JSON(ctx, "POST", "/api/v1/commands/"+url.PathEscape(args[0])+"/"+verb, nil, &out)
	case "retire":
		need(1)
		err = sess.JSON(ctx, "DELETE", "/api/v1/devices/"+url.PathEscape(args[0]), nil, nil)
	case "token":
		fs := flag.NewFlagSet("token", flag.ExitOnError)
		group := fs.String("group", "default", "device group")
		uses := fs.Int("uses", 1, "devices that may enroll with it")
		hours := fs.Int("hours", 24, "hours it stays valid")
		fs.Parse(args)
		err = sess.JSON(ctx, "POST", "/api/v1/tokens", map[string]any{"group": *group, "uses": *uses, "hours": *hours}, &out)
	case "policy":
		if len(args) == 0 {
			err = sess.JSON(ctx, "GET", "/api/v1/policies", nil, &out)
			break
		}
		kv, perr := pairs(args[1:])
		if perr != nil {
			return perr
		}
		err = sess.JSON(ctx, "PUT", "/api/v1/policies/"+url.PathEscape(args[0]), kv, nil)
	case "audit":
		err = sess.JSON(ctx, "GET", "/api/v1/audit", nil, &out)
	}
	if err != nil {
		return mapErr(err)
	}
	if out != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return nil
}

func agentCmd(args []string) error {
	if len(args) < 1 {
		usage()
	}
	fs := flag.NewFlagSet("agent "+args[0], flag.ExitOnError)
	dir := fs.String("dir", "/var/lib/sinty-it-agent", "agent state directory")
	srv := fs.String("server", "", "sinty-it address")
	tok := fs.String("token", "", "enrollment token")
	host, _ := os.Hostname()
	name := fs.String("name", host, "device name shown to administrators")
	every := fs.Duration("every", 5*time.Minute, "time between check-ins")
	config := fs.String("config", "/etc/sinty-it/agent.json", "paths of the system services, when they differ from Sinty OS")
	fs.Parse(args[1:])
	paths, err := platform.Load(*config)
	if err != nil {
		return err
	}
	p := platform.New(paths)
	a := &agent.Agent{Dir: *dir, Handlers: p.Handlers(), Facts: p.Facts, Policy: p.ApplyPolicy}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "enroll":
		if *srv == "" || *tok == "" {
			return exitError{2, "-server and -token are required"}
		}
		id, err := a.Enroll(ctx, *srv, *tok, *name)
		if errors.Is(err, agent.ErrRefused) {
			return exitError{5, "the enrollment token was refused"}
		}
		if err != nil {
			return err
		}
		fmt.Printf("Enrolled as %s in group %s\n", id.Device, id.Group)
		return nil
	case "run":
		err := a.Loop(ctx, *every, func(f string, v ...any) { log.Printf(f, v...) })
		if errors.Is(err, agent.ErrRefused) {
			return exitError{5, "the server no longer knows this device"}
		}
		if errors.Is(err, agent.ErrNotEnrolled) {
			return exitError{3, "this device is not enrolled: run sinty-it agent enroll"}
		}
		return err
	case "status":
		id, err := a.Enrolled()
		if errors.Is(err, agent.ErrNotEnrolled) {
			return exitError{3, "this device is not enrolled"}
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"server": id.Server, "device": id.Device, "group": id.Group})
	}
	usage()
	return nil
}
