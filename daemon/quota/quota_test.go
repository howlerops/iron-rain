package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeAnthropicParsesRateLimitHeaders(t *testing.T) {
	reset := time.Now().Add(45 * time.Second).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("missing/wrong api key header: %q", r.Header.Get("x-api-key"))
		}
		w.Header().Set("anthropic-ratelimit-requests-remaining", "42")
		w.Header().Set("anthropic-ratelimit-tokens-remaining", "180000")
		w.Header().Set("anthropic-ratelimit-requests-reset", reset.Format(time.RFC3339))
		w.WriteHeader(200)
		w.Write([]byte(`{"input_tokens":8}`))
	}))
	defer srv.Close()

	p := &Prober{HTTP: srv.Client(), AnthropicBase: srv.URL}
	q, err := p.Probe(context.Background(), "claude-code", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"})
	if err != nil {
		t.Fatal(err)
	}
	if !q.Available || q.RequestsRemaining != 42 || q.TokensRemaining != 180000 {
		t.Fatalf("parsed quota wrong: %+v", q)
	}
	if !q.ResetAt.Equal(reset) {
		t.Errorf("ResetAt = %v, want %v", q.ResetAt, reset)
	}
}

func TestProbeOpenAIParsesRelativeReset(t *testing.T) {
	timeNow = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	defer func() { timeNow = time.Now }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer sk-oai-test" {
			t.Errorf("wrong auth: %q", r.Header.Get("authorization"))
		}
		w.Header().Set("x-ratelimit-remaining-requests", "9")
		w.Header().Set("x-ratelimit-remaining-tokens", "8000")
		w.Header().Set("x-ratelimit-reset-requests", "6m0s")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p := &Prober{HTTP: srv.Client(), OpenAIBase: srv.URL}
	q, err := p.Probe(context.Background(), "codex", map[string]string{"OPENAI_API_KEY": "sk-oai-test"})
	if err != nil {
		t.Fatal(err)
	}
	if q.RequestsRemaining != 9 || q.TokensRemaining != 8000 {
		t.Fatalf("parsed wrong: %+v", q)
	}
	want := time.Date(2026, 1, 1, 12, 6, 0, 0, time.UTC)
	if !q.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v (now + 6m)", q.ResetAt, want)
	}
}

func TestProbeNoKeyReturnsErrNoKey(t *testing.T) {
	p := New()
	q, err := p.Probe(context.Background(), "claude-code", map[string]string{})
	if err != ErrNoKey {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
	if q.Available {
		t.Error("should not be available without a key")
	}
}

func TestProbeInvalidKey401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	p := &Prober{HTTP: srv.Client(), AnthropicBase: srv.URL}
	q, _ := p.Probe(context.Background(), "claude-code", map[string]string{"ANTHROPIC_API_KEY": "bad"})
	if q.Available {
		t.Error("401 should mark unavailable")
	}
	if q.Note == "" {
		t.Error("expected a note explaining the invalid key")
	}
}
