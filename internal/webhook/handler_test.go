package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/joenas/matrix-slackhooks/internal/store"
)

type recordingSender struct {
	hook    *store.Hook
	payload *Payload
}

func (s *recordingSender) Send(ctx context.Context, hook *store.Hook, payload *Payload) error {
	s.hook = hook
	s.payload = payload
	return nil
}

func newTestHandler(t *testing.T) (*Handler, *recordingSender, *store.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	err = db.InsertHook(&store.Hook{Token: "goodtoken", RoomID: "!room:example.com", Label: "Test Hook"})
	if err != nil {
		t.Fatalf("insert hook: %v", err)
	}
	sender := &recordingSender{}
	handler := &Handler{Store: db, Sender: sender, Log: zerolog.Nop()}
	return handler, sender, db
}

func doRequest(handler *Handler, method, target string, body string, contentType string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Mount(mux, handler)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandlerSuccess(t *testing.T) {
	handler, sender, _ := newTestHandler(t)
	rec := doRequest(handler, http.MethodPost, "/hooks/goodtoken", `{"text": "hi", "username": "Bob"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "ok" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
	if sender.hook == nil || sender.hook.RoomID != "!room:example.com" {
		t.Errorf("unexpected hook: %+v", sender.hook)
	}
	if sender.payload.SourceName() != "Bob" {
		t.Errorf("unexpected payload: %+v", sender.payload)
	}
}

func TestHandlerSlackStylePath(t *testing.T) {
	handler, sender, _ := newTestHandler(t)
	rec := doRequest(handler, http.MethodPost, "/services/T000/B000/goodtoken", `{"text": "hi"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	if sender.hook == nil {
		t.Fatal("sender not called")
	}
}

func TestHandlerUnknownToken(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	rec := doRequest(handler, http.MethodPost, "/hooks/nope", `{"text": "hi"}`, "application/json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandlerBadPayload(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	rec := doRequest(handler, http.MethodPost, "/hooks/goodtoken", `{"text": ""}`, "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty text, got %d", rec.Code)
	}
	rec = doRequest(handler, http.MethodPost, "/hooks/goodtoken", "{invalid json", "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d", rec.Code)
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	rec := doRequest(handler, http.MethodGet, "/hooks/goodtoken", "", "")
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("expected 405/404 for GET, got %d", rec.Code)
	}
}

// blockingSender blocks until its context is done, standing in for a hung
// homeserver, and returns the context error like a real client would.
type blockingSender struct{}

func (blockingSender) Send(ctx context.Context, _ *store.Hook, _ *Payload) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestHandlerSendTimeout(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	handler.Sender = blockingSender{}
	defer func(old time.Duration) { sendTimeout = old }(sendTimeout)
	sendTimeout = 20 * time.Millisecond

	rec := doRequest(handler, http.MethodPost, "/hooks/goodtoken", `{"text": "hi"}`, "application/json")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on send timeout, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Matrix homeserver unavailable") {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

type deadlineRecorderSender struct{ hadDeadline *bool }

func (s deadlineRecorderSender) Send(ctx context.Context, _ *store.Hook, _ *Payload) error {
	_, ok := ctx.Deadline()
	*s.hadDeadline = ok
	return nil
}

func TestHandlerSendHasDeadline(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	had := false
	handler.Sender = deadlineRecorderSender{hadDeadline: &had}
	rec := doRequest(handler, http.MethodPost, "/hooks/goodtoken", `{"text": "hi"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !had {
		t.Error("Send should receive a context with a deadline")
	}
}
