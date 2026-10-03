package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/stuffam/drawbridge/internal/views"
)

// The connection to AdGuard Home (docs/PLAN.md §6.3). The password can be set and never read back.

// adguardView is the saved connection and how its sync is doing.
func (h *handler) adguardView(r *http.Request) (views.AdGuardConnection, error) {
	c, err := h.svc.AdGuard(r.Context())
	if err != nil {
		return views.AdGuardConnection{}, err
	}
	sync, err := h.svc.AdGuardSync(r.Context())
	if err != nil {
		return views.AdGuardConnection{}, err
	}
	return views.NewAdGuardConnection(c, sync), nil
}

func (h *handler) getAdGuard(w http.ResponseWriter, r *http.Request) {
	v, err := h.adguardView(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) putAdGuard(w http.ResponseWriter, r *http.Request) {
	var req views.AdGuardRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if _, err := h.svc.UpdateAdGuard(r.Context(), req.Service()); err != nil {
		h.fail(w, err)
		return
	}
	v, err := h.adguardView(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) deleteAdGuard(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveAdGuard(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	v, err := h.adguardView(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// syncAdGuard runs a pass of the name sync now, and says how it went.
func (h *handler) syncAdGuard(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.SyncAdGuardNow(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewAdGuardSync(st))
}

func (h *handler) testAdGuard(w http.ResponseWriter, r *http.Request) {
	// The body is optional: with none, the saved connection is tested.
	var req views.AdGuardRequest
	if err := decode(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		h.fail(w, err)
		return
	}
	res, err := h.svc.TestAdGuard(r.Context(), req.Service())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewAdGuardTest(res))
}

// clientDNSLog returns what a client has looked up lately, from AdGuard Home's query log.
func (h *handler) clientDNSLog(w http.ResponseWriter, r *http.Request) {
	limit, err := views.ParseDNSLogLimit(r.URL.Query())
	if err != nil {
		h.fail(w, err)
		return
	}
	log, err := h.svc.ClientDNSLog(r.Context(), clientRef(r), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewDNSLog(log))
}
