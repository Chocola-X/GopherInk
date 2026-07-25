package defaulttheme

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
)

func handleView(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
	handleContentCounter(rt, w, r, "views")
}

func handleLike(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
	handleContentCounter(rt, w, r, "likes")
}

func handleContentCounter(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request, field string) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	if !strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if rt == nil || rt.ValidateCSRF == nil || !rt.ValidateCSRF(r, "public") {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if rt.IncrementContentFieldInt == nil {
		http.Error(w, "runtime unavailable", http.StatusInternalServerError)
		return
	}
	cid, err := strconv.ParseInt(r.FormValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		http.Error(w, "invalid content id", http.StatusBadRequest)
		return
	}
	content, err := rt.GetContent(r.Context(), cid)
	if err != nil || (content.Type != models.ContentTypePost && content.Type != models.ContentTypePage) || content.Status != models.ContentStatusPost {
		http.NotFound(w, r)
		return
	}
	value, err := rt.IncrementContentFieldInt(r.Context(), cid, field, 1)
	if err != nil {
		http.Error(w, "update content counter failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]int64{field: value})
}
