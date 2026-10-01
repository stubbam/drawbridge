package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/views"
)

// fail maps an error to a status: the caller's mistakes are 4xx, everything else 500.
func (h *handler) fail(w http.ResponseWriter, err error) {
	status := views.ErrorStatus(err)
	var limited *service.RateLimitedError
	if errors.As(err, &limited) {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(limited.Wait.Seconds()))))
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		h.log.Error("API request failed", "err", err)
		// The details are in the journal; they're no business of the network.
		msg = "internal error; the details are in the journal (journalctl -u drawbridge)"
	}
	writeJSON(w, status, views.Error{Error: msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &model.InvalidError{Err: fmt.Errorf("bad request body: %w", err)}
	}
	if dec.More() {
		return &model.InvalidError{Err: errors.New("bad request body: more than one JSON value")}
	}
	return nil
}

func clientRef(r *http.Request) store.Ref { return store.ByID(r.PathValue("id")) }

// Setup and login.

func (h *handler) setupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := h.svc.SetupNeeded(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.SetupStatus{Needed: needed})
}

func (h *handler) setup(w http.ResponseWriter, r *http.Request) {
	var req views.SetupRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	login, err := h.svc.CompleteSetup(r.Context(), req.Token, req.Username, req.Password, r.UserAgent())
	if err != nil {
		h.fail(w, err)
		return
	}
	setSessionCookie(w, login.Token, login.Session.ExpiresAt)
	writeJSON(w, http.StatusCreated, views.Me{User: views.User(login.User), Session: views.Session(login.Session, login.Session.ID)})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var req views.LoginRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	login, err := h.svc.Login(r.Context(), req.Username, req.Password, r.UserAgent())
	if err != nil {
		h.fail(w, err)
		return
	}
	setSessionCookie(w, login.Token, login.Session.ExpiresAt)
	writeJSON(w, http.StatusOK, views.Me{User: views.User(login.User), Session: views.Session(login.Session, login.Session.ID)})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), sessionFrom(r.Context()).session); err != nil {
		h.fail(w, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, views.Me{User: views.User(s.user), Session: views.Session(s.session, s.session.ID)})
}

func (h *handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req views.PasswordChange
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	s := sessionFrom(r.Context())
	if err := h.svc.ChangePassword(r.Context(), s.user, s.session, req.CurrentPassword, req.NewPassword); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) sessions(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r.Context())
	list, err := h.svc.ListSessions(r.Context(), s.user.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]views.SessionView, len(list))
	for i, sess := range list {
		out[i] = views.Session(sess, s.session.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r.Context())
	id := r.PathValue("id")
	if err := h.svc.RevokeSession(r.Context(), s.user.ID, id); err != nil {
		h.fail(w, err)
		return
	}
	if id == s.session.ID {
		clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Server settings.

func (h *handler) getServer(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Settings(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.Settings(s))
}

func (h *handler) patchServer(w http.ResponseWriter, r *http.Request) {
	var p views.SettingsPatch
	if err := decode(w, r, &p); err != nil {
		h.fail(w, err)
		return
	}
	s, applied, err := h.svc.UpdateSettings(r.Context(), p.Service())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewSettingsResult(s, applied))
}

func (h *handler) dnsCheck(w http.ResponseWriter, r *http.Request) {
	probes, err := h.svc.ProbeDNS(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewDNSCheck(probes))
}

func (h *handler) serverStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Status(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewServerStatus(st))
}

// Clients.

func (h *handler) listClients(w http.ResponseWriter, r *http.Request) {
	clients, err := h.svc.Clients(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.Statuses(clients))
}

func (h *handler) addClient(w http.ResponseWriter, r *http.Request) {
	var req views.NewClientRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	c, applied, err := h.svc.AddClient(r.Context(), req.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location", "/api/clients/"+c.ID)
	writeJSON(w, http.StatusCreated, views.NewClientResult(c, applied))
}

func (h *handler) getClient(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Client(r.Context(), clientRef(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.Status(c))
}

func (h *handler) patchClient(w http.ResponseWriter, r *http.Request) {
	var p views.ClientPatch
	if err := decode(w, r, &p); err != nil {
		h.fail(w, err)
		return
	}
	c, err := h.svc.Client(r.Context(), clientRef(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	if p.Name != nil {
		if c.Client, err = h.svc.RenameClient(r.Context(), clientRef(r), *p.Name); err != nil {
			h.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, views.ClientResult{Client: views.Status(c)})
}

func (h *handler) deleteClient(w http.ResponseWriter, r *http.Request) {
	c, applied, err := h.svc.DeleteClient(r.Context(), clientRef(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewClientResult(c, applied))
}

func (h *handler) pauseClient(w http.ResponseWriter, r *http.Request)  { h.setEnabled(w, r, false) }
func (h *handler) resumeClient(w http.ResponseWriter, r *http.Request) { h.setEnabled(w, r, true) }

func (h *handler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	c, applied, err := h.svc.SetEnabled(r.Context(), clientRef(r), enabled)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewClientResult(c, applied))
}

// clientConfig downloads a client's config. It holds the client's private key, so it's
// never cached, and it's always a download, never shown by the browser.
func (h *handler) clientConfig(w http.ResponseWriter, r *http.Request) {
	c, conf, err := h.svc.Config(r.Context(), clientRef(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": views.ConfigFileName(c.Name)}))
	// A download (Content-Disposition: attachment) of plain text with nosniff, so no
	// browser renders it as a page.
	_, _ = io.WriteString(w, conf) //nolint:gosec // G705, see above.
}

// Traffic history and session history.

func (h *handler) clientTraffic(w http.ResponseWriter, r *http.Request) {
	resolution, lookback, err := views.ParseTrafficRange(r.URL.Query().Get("range"))
	if err != nil {
		h.fail(w, err)
		return
	}
	samples, err := h.svc.ClientTraffic(r.Context(), clientRef(r), resolution, lookback)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.TrafficSamples(samples))
}

func (h *handler) clientsTraffic(w http.ResponseWriter, r *http.Request) {
	resolution, lookback, err := views.ParseTrafficRange(r.URL.Query().Get("range"))
	if err != nil {
		h.fail(w, err)
		return
	}
	history, err := h.svc.ClientsTraffic(r.Context(), resolution, lookback)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewTrafficHistory(history))
}

func (h *handler) totalTraffic(w http.ResponseWriter, r *http.Request) {
	resolution, lookback, err := views.ParseTrafficRange(r.URL.Query().Get("range"))
	if err != nil {
		h.fail(w, err)
		return
	}
	samples, err := h.svc.TotalTraffic(r.Context(), resolution, lookback)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.TrafficSamples(samples))
}

func (h *handler) clientSessions(w http.ResponseWriter, r *http.Request) {
	before, limit, err := views.ParseSessionHistoryFilter(r.URL.Query())
	if err != nil {
		h.fail(w, err)
		return
	}
	sessions, err := h.svc.ClientSessionHistory(r.Context(), clientRef(r), before, limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.ClientSessions(sessions))
}

// Events.

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	f, err := views.ParseEventFilter(r.URL.Query())
	if err != nil {
		h.fail(w, err)
		return
	}
	f.ClientID = r.URL.Query().Get("client")
	events, err := h.svc.Events(r.Context(), f)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.Events(events))
}
