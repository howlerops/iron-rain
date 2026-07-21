package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	if got := Format("Agent finished", "on my-project", "AGENT_FINISHED"); !strings.HasPrefix(got, "✅ *Agent finished*") || !strings.Contains(got, "on my-project") {
		t.Errorf("format = %q", got)
	}
	if got := Format("Untitled", "", "SOMETHING_ELSE"); got != "🐺 *Untitled*" {
		t.Errorf("fallback emoji/format = %q", got)
	}
}

func TestPost(t *testing.T) {
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(b, &payload)
		gotText = payload["text"]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	if err := New(srv.URL).Post(context.Background(), "hello"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotText != "hello" {
		t.Errorf("posted text = %q, want hello", gotText)
	}

	// A non-2xx webhook response is an error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer bad.Close()
	if err := New(bad.URL).Post(context.Background(), "x"); err == nil {
		t.Error("expected error on non-2xx webhook response")
	}
}
