// Package agentbus lets the people using a device enroll it in their
// organization from Settings. It runs as root on the system bus and asks polkit
// before enrolling the device or taking it out of management.
package agentbus

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/singularityos-lab/sinty-it/agent"
)

const (
	Name      = "dev.sinty.ITAgent1"
	Path      = dbus.ObjectPath("/dev/sinty/ITAgent1")
	Interface = "dev.sinty.ITAgent1"

	ActionEnroll = "dev.sinty.it.enroll"
	ActionLeave  = "dev.sinty.it.leave"
)

type Service struct {
	Agent *agent.Agent
	Check func(sender dbus.Sender, action string) *dbus.Error
	Start func(ctx context.Context) error
	Stop  func(ctx context.Context) error
	Emit  func(signal string, args ...any)
	Logf  func(format string, args ...any)
}

func failed(err error) *dbus.Error {
	return dbus.NewError(Interface+".Error.Failed", []any{err.Error()})
}

func invalid(msg string) *dbus.Error {
	return dbus.NewError(Interface+".Error.Invalid", []any{msg})
}

// Status reports whether the device is managed, by which server, and its
// device and group identifiers there.
func (s *Service) Status() (bool, string, string, string, *dbus.Error) {
	id, err := s.Agent.Enrolled()
	if errors.Is(err, agent.ErrNotEnrolled) {
		return false, "", "", "", nil
	}
	if err != nil {
		return false, "", "", "", failed(err)
	}
	return true, id.Server, id.Device, id.Group, nil
}

// Server reduces what a person pastes to a server address: the address
// itself, or an enrollment link carrying the token after it.
func Server(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", errors.New("not a server address")
	}
	if u.Scheme != "https" {
		return "", "", errors.New("the server must use https")
	}
	token := u.Query().Get("token")
	if token == "" && strings.HasPrefix(u.Fragment, "sit_") {
		token = u.Fragment
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/enroll")
	return "https://" + u.Host + path, token, nil
}

func (s *Service) Enroll(sender dbus.Sender, server, token, name string) (string, string, *dbus.Error) {
	addr, linked, err := Server(server)
	if err != nil {
		return "", "", invalid(err.Error())
	}
	token = strings.TrimSpace(token)
	if token == "" {
		token = linked
	}
	if token == "" {
		return "", "", invalid("an enrollment code is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", invalid("a device name is required")
	}
	if e := s.Check(sender, ActionEnroll); e != nil {
		return "", "", e
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id, err := s.Agent.Enroll(ctx, addr, token, name)
	if errors.Is(err, agent.ErrRefused) {
		return "", "", dbus.NewError(Interface+".Error.Refused", []any{"the organization refused this enrollment code"})
	}
	if err != nil {
		return "", "", failed(err)
	}
	if s.Start != nil {
		if err := s.Start(ctx); err != nil {
			return "", "", failed(err)
		}
	}
	s.emit()
	return id.Device, id.Group, nil
}

func (s *Service) Leave(sender dbus.Sender) *dbus.Error {
	if _, err := s.Agent.Enrolled(); errors.Is(err, agent.ErrNotEnrolled) {
		return nil
	}
	if e := s.Check(sender, ActionLeave); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if s.Stop != nil {
		if err := s.Stop(ctx); err != nil {
			return failed(err)
		}
	}
	if err := s.Agent.Leave(ctx); err != nil && !errors.Is(err, agent.ErrUnannounced) {
		return failed(err)
	} else if err != nil && s.Logf != nil {
		s.Logf("%v", err)
	}
	s.emit()
	return nil
}

func (s *Service) emit() {
	if s.Emit != nil {
		s.Emit("Changed")
	}
}

const introspection = `<interface name="` + Interface + `">
  <method name="Status">
    <arg name="enrolled" type="b" direction="out"/>
    <arg name="server" type="s" direction="out"/>
    <arg name="device" type="s" direction="out"/>
    <arg name="group" type="s" direction="out"/>
  </method>
  <method name="Enroll">
    <arg name="server" type="s" direction="in"/>
    <arg name="token" type="s" direction="in"/>
    <arg name="name" type="s" direction="in"/>
    <arg name="device" type="s" direction="out"/>
    <arg name="group" type="s" direction="out"/>
  </method>
  <method name="Leave"/>
  <signal name="Changed"/>
</interface>`

// Export publishes s on conn and takes the bus name.
func Export(conn *dbus.Conn, s *Service) error {
	if s.Emit == nil {
		s.Emit = func(signal string, args ...any) { conn.Emit(Path, Interface+"."+signal, args...) }
	}
	if err := conn.Export(s, Path, Interface); err != nil {
		return err
	}
	data := introspect.Introspectable(introspect.IntrospectDeclarationString + "<node>" + introspection + introspect.IntrospectDataString + "</node>")
	if err := conn.Export(data, Path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	reply, err := conn.RequestName(Name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New(Name + " is already taken")
	}
	return nil
}
