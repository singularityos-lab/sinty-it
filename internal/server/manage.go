package server

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-identity/rp"
	"github.com/singularityos-lab/sinty-it/internal/state"
)

type tokenView struct {
	ID      string    `json:"id"`
	Group   string    `json:"group"`
	By      string    `json:"by"`
	Uses    int       `json:"uses"`
	Expires time.Time `json:"expires"`
}

func tokenID(t state.Token) string {
	return t.Hash[:16]
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, who string) {
	list := []tokenView{}
	now := s.Now()
	err := s.Store.Update(func(st *state.State) error {
		st.Tokens = slices.DeleteFunc(st.Tokens, func(t state.Token) bool { return !now.Before(t.Expires) || t.Uses <= 0 })
		for _, t := range st.Tokens {
			list = append(list, tokenView{ID: tokenID(t), Group: t.Group, By: t.By, Uses: t.Uses, Expires: t.Expires})
		}
		return nil
	})
	if s.fail(w, err) {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Expires.Before(list[j].Expires) })
	rp.Reply(w, map[string]any{"tokens": list})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, who string) {
	id := r.PathValue("id")
	var group string
	err := s.Store.Update(func(st *state.State) error {
		i := slices.IndexFunc(st.Tokens, func(t state.Token) bool { return len(id) == 16 && tokenID(t) == id })
		if i < 0 {
			return errNotFound
		}
		group = st.Tokens[i].Group
		st.Tokens = slices.Delete(st.Tokens, i, i+1)
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "token.revoke", group, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) editDevice(w http.ResponseWriter, r *http.Request, who string) {
	var in struct {
		Name  *string `json:"name"`
		Group *string `json:"group"`
	}
	if !decode(w, r, 1<<12, &in) {
		return
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 64 {
			rp.Fail(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	if in.Group != nil && !groupName.MatchString(*in.Group) {
		rp.Fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id := r.PathValue("id")
	detail := map[string]string{}
	var v deviceView
	err := s.Store.Update(func(st *state.State) error {
		d := st.Devices[id]
		if d == nil || d.Retired {
			return errNotFound
		}
		if in.Name != nil && *in.Name != d.Name {
			detail["name"] = *in.Name
			d.Name = *in.Name
		}
		if in.Group != nil && *in.Group != d.Group {
			detail["group"] = *in.Group
			d.Group = *in.Group
		}
		v = s.view(d)
		return nil
	})
	if s.fail(w, err) {
		return
	}
	if len(detail) > 0 {
		s.record(who, "device.edit", id, detail)
	}
	rp.Reply(w, v)
}

func (s *Server) dropPolicy(w http.ResponseWriter, r *http.Request, who string) {
	group := r.PathValue("group")
	err := s.Store.Update(func(st *state.State) error {
		if st.Policies[group] == nil {
			return errNotFound
		}
		delete(st.Policies, group)
		return nil
	})
	if s.fail(w, err) {
		return
	}
	s.record(who, "policy.delete", group, nil)
	w.WriteHeader(http.StatusNoContent)
}
