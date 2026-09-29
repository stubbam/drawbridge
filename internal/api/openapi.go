package api

import (
	_ "embed" // openapi.json
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/stuffam/drawbridge/internal/version"
)

// openAPIDoc documents every route in the routes table; a test keeps them in step.
//
//go:embed openapi.json
var openAPIDoc []byte

var openAPIOnce = sync.OnceValues(func() ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(openAPIDoc, &doc); err != nil {
		return nil, err
	}
	// OpenAPI versions have no v prefix (CLAUDE.md, "Conventions").
	if info, ok := doc["info"].(map[string]any); ok {
		info["version"] = strings.TrimPrefix(version.Version, "v")
	}
	return json.MarshalIndent(doc, "", "  ")
})

func (h *handler) openAPI(w http.ResponseWriter, _ *http.Request) {
	doc, err := openAPIOnce()
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(doc)
}
