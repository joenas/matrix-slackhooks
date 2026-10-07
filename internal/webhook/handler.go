package webhook

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/joenas/matrix-slackhooks/internal/store"
)

const maxBodySize = 1 << 20

// Sender delivers a parsed webhook payload into a Matrix room.
type Sender interface {
	Send(ctx context.Context, hook *store.Hook, payload *Payload) error
}

type Handler struct {
	Store  *store.DB
	Sender Sender
	Log    zerolog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		// Slack-style /services/<a>/<b>/<token> paths: the token is the last segment.
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		token = segments[len(segments)-1]
	}
	log := h.Log.With().Str("token", tokenLogPrefix(token)).Logger()
	hook, err := h.Store.GetHook(token)
	if err != nil {
		log.Error().Err(err).Msg("Failed to look up webhook")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	} else if hook == nil {
		http.Error(w, "Webhook not found", http.StatusNotFound)
		return
	}
	log = log.With().Str("room_id", hook.RoomID.String()).Logger()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	} else if int64(len(body)) > maxBodySize {
		http.Error(w, "Payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	payload, err := ParsePayload(r.Header.Get("Content-Type"), body)
	if err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	} else if !payload.Valid() {
		http.Error(w, "Missing text", http.StatusBadRequest)
		return
	}
	if err = h.Sender.Send(r.Context(), hook, payload); err != nil {
		log.Error().Err(err).Msg("Failed to send webhook message")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// tokenLogPrefix returns a short non-secret prefix of a webhook token for
// log context, so full tokens never end up in the logs.
func tokenLogPrefix(token string) string {
	if len(token) > 6 {
		return token[:6]
	}
	return token
}

// Mount registers the webhook routes on the given mux.
func Mount(mux *http.ServeMux, handler http.Handler) {
	mux.Handle("POST /hooks/{token}", handler)
	mux.Handle("POST /hooks/{token}/", handler)
	mux.Handle("POST /services/", handler)
}
