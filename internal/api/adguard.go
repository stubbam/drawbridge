package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/stuffam/drawbridge/internal/views"
)

// The connection to AdGuard Home (docs/PLAN.md §6.3). The password can be set and never read back.

func (h *handler) getAdGuard(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.AdGuard(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewAdGuardConnection(c))
}

func (h *handler) putAdGuard(w http.ResponseWriter, r *http.Request) {
	var req views.AdGuardRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	c, err := h.svc.UpdateAdGuard(r.Context(), req.Service())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewAdGuardConnection(c))
}

func (h *handler) deleteAdGuard(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveAdGuard(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	c, err := h.svc.AdGuard(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.NewAdGuardConnection(c))
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
