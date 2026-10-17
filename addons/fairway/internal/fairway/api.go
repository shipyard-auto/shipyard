package fairway

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

// fairway's own API under /_shipyard/v1:
//
//	POST   /events               publish an event (loopback only: local producers)
//	GET    /subscriptions        a signed client lists its subscriptions
//	POST   /subscriptions        a signed client subscribes {topic, url}
//	DELETE /subscriptions/{id}   a signed client unsubscribes

func writeAPI(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeAPI(w, status, map[string]string{"error": msg})
}

// handlePublish accepts events from producers on this machine.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		apiError(w, http.StatusForbidden, "events can only be published from this machine")
		return
	}
	if s.events == nil {
		apiError(w, http.StatusServiceUnavailable, "events are not enabled")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxEventBytes+1))
	if err != nil || len(raw) > maxEventBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "event too large (max 1 MiB)")
		return
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		apiError(w, http.StatusBadRequest, "invalid event JSON: "+err.Error())
		return
	}
	if err := ValidateTopic(ev.Topic); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ev.Scope != "" {
		if err := signing.ValidateScope(ev.Scope); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(ev.Data) == 0 {
		ev.Data = json.RawMessage("null")
	}
	now := time.Now().UTC()
	if ev.ID == "" {
		ev.ID = NewEventID(now)
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = now
	}
	n := s.events.Publish(ev)
	writeAPI(w, http.StatusAccepted, map[string]any{"id": ev.ID, "subscribers": n})
}

// apiClient authenticates a signed request to fairway's own API.
func (s *Server) apiClient(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	if s.events == nil || s.authDeps.Clients == nil {
		apiError(w, http.StatusServiceUnavailable, "events are not enabled")
		return Identity{}, false
	}
	auth := &signedAuth{clients: s.authDeps.Clients, nonces: s.authDeps.Nonces, now: time.Now}
	authed, err := auth.VerifyContext(r)
	if err != nil {
		if ae, ok := IsAuthError(err); ok {
			apiError(w, ae.Status, ae.Reason)
		} else {
			apiError(w, http.StatusUnauthorized, "authentication failed")
		}
		return Identity{}, false
	}
	id, _ := IdentityFrom(authed.Context())
	yardlogs.MarkClient(r, id.ClientID)
	return id, true
}

func (s *Server) handleSubscriptionList(w http.ResponseWriter, r *http.Request) {
	id, ok := s.apiClient(w, r)
	if !ok {
		return
	}
	writeAPI(w, http.StatusOK, s.events.Views(id.ClientID))
}

func (s *Server) handleSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.apiClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Topic string `json:"topic"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	sub, err := s.events.Subscribe(id.ClientID, body.Topic, body.URL, false)
	switch {
	case err == nil:
		writeAPI(w, http.StatusCreated, sub)
	case strings.Contains(err.Error(), "lacks scope"):
		apiError(w, http.StatusForbidden, err.Error())
	default:
		apiError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) handleSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.apiClient(w, r)
	if !ok {
		return
	}
	if err := s.events.Subs.Delete(r.PathValue("id"), id.ClientID); err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			apiError(w, http.StatusNotFound, "subscription not found")
			return
		}
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
