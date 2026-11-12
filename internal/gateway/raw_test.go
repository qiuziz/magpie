package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestRawRequestCarried(t *testing.T) {
	raw := &rawRequest{body: []byte(`{"model":"deepseek-v4.1-flash"}`), header: http.Header{"User-Agent": {"CLI/2.180.0 CodeBuddy/2.180.0"}}}
	ctx := withRawRequest(context.Background(), raw)
	if got := rawRequestOf(ctx); got != raw {
		t.Fatalf("rawRequestOf = %p, want %p", got, raw)
	}
	if got := rawRequestOf(context.Background()); got != nil {
		t.Fatalf("rawRequestOf(background) = %+v, want nil", got)
	}
}

func TestRawHeaderDropped(t *testing.T) {
	// the client's own fingerprint stays
	for _, k := range []string{"User-Agent", "X-Ide-Type", "X-Agent-Purpose", "X-Agent-Intent", "X-Custom", "Accept"} {
		if rawHeaderDropped(k) {
			t.Errorf("rawHeaderDropped(%q) = true, want false", k)
		}
	}
	// the hop's, the transport's and the credential's don't go on
	for _, k := range []string{"Host", "Connection", "Content-Length", "Accept-Encoding", "Authorization", "X-Api-Key", "Cookie", "X-Magpie-Raw"} {
		if !rawHeaderDropped(k) {
			t.Errorf("rawHeaderDropped(%q) = false, want true", k)
		}
	}
}

func TestRawForwardSendsTheClientsOwn(t *testing.T) {
	var got struct {
		header http.Header
		body   []byte
		path   string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	p := provider.Provider{ID: "raw", Name: "Raw", Chat: upstream.URL, Key: "key", Raw: true,
		Headers: map[string]string{"X-User-Id": "uid-lxh", "X-Domain": "copilot.tencent.com"}}
	s := &Server{client: upstream.Client()}
	body := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"kept"}]}`)
	raw := &rawRequest{body: body, header: http.Header{
		"User-Agent":      {"CLI/2.180.0 CodeBuddy/2.180.0"},
		"X-Ide-Type":      {"CodeBuddy"},
		"X-Agent-Purpose": {"conversation"},
		"X-Custom":        {"kept"},
		"Content-Length":  {"999"},
		"Authorization":   {"Bearer client"},
	}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(withRawRequest(r.Context(), raw))
	status, msg := s.rawForward(rec, r, p, "deepseek-v4.1-flash", raw, &Call{Time: time.Now(), Model: "deepseek-v4.1-flash"})
	if status != http.StatusOK || msg != "" {
		t.Fatalf("rawForward = %d %q, want 200", status, msg)
	}
	if got.path != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", got.path)
	}
	if !bytes.Equal(got.body, body) {
		t.Errorf("body = %s, want the client's bytes %s", got.body, body)
	}
	for k, want := range map[string]string{
		"User-Agent":      "CLI/2.180.0 CodeBuddy/2.180.0",
		"X-Ide-Type":      "CodeBuddy",
		"X-Agent-Purpose": "conversation",
		"X-Custom":        "kept",
		"X-User-Id":       "uid-lxh",
		"X-Domain":        "copilot.tencent.com",
	} {
		if v := got.header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if v := got.header.Get("Content-Length"); v == "999" {
		t.Error("Content-Length went as the client's 999, want the body's own")
	}
	if v := got.header.Get("Authorization"); strings.Contains(v, "client") || v == "" {
		t.Errorf("Authorization = %q, want the provider's own key", v)
	}
	if rec.Body.String() != "data: [DONE]\n\n" {
		t.Errorf("reply = %q, want the upstream's stream", rec.Body.String())
	}
}

func TestRawForwardKeepsTheClients31Headers(t *testing.T) {
	b, err := os.ReadFile("testdata/cbc-raw-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Headers) != 31 {
		t.Fatalf("fixture has %d headers, want 31", len(fixture.Headers))
	}

	var got http.Header
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	p := provider.Provider{ID: "raw31", Name: "Raw31", Chat: upstream.URL, Key: "key", Raw: true,
		Headers: map[string]string{"X-User-Id": "uid-account", "X-Domain": "copilot.tencent.com"}}
	s := &Server{client: upstream.Client()}
	h := http.Header{}
	for k, v := range fixture.Headers {
		h.Set(k, v)
	}
	raw := &rawRequest{body: []byte(fixture.Body), header: h}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(withRawRequest(r.Context(), raw))
	status, msg := s.rawForward(rec, r, p, "deepseek-v4.1-flash", raw, &Call{Time: time.Now(), Model: "deepseek-v4.1-flash"})
	if status != http.StatusOK || msg != "" {
		t.Fatalf("rawForward = %d %q, want 200", status, msg)
	}
	if string(gotBody) != fixture.Body {
		t.Errorf("body = %q, want the client's bytes", gotBody)
	}

	// the five allowed changes: three account headers, Content-Length and
	// Host/Content-Length's transport. Accept-Encoding is the transport's.
	changed := map[string]bool{"Authorization": true, "Content-Length": true, "Accept-Encoding": true, "X-User-Id": true, "X-Domain": true}
	for k, want := range fixture.Headers {
		if changed[http.CanonicalHeaderKey(k)] {
			continue
		}
		if v := got.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if v := got.Get("Authorization"); v != "Bearer key" {
		t.Errorf("Authorization = %q, want the provider's key", v)
	}
	if v := got.Get("X-User-Id"); v != "uid-account" {
		t.Errorf("X-User-Id = %q, want the provider's", v)
	}
	if v := got.Get("X-Domain"); v != "copilot.tencent.com" {
		t.Errorf("X-Domain = %q, want the provider's", v)
	}
	if v := got.Get("Content-Length"); v == fixture.Headers["Content-Length"] {
		t.Errorf("Content-Length = %q, want the body's own", v)
	}
	if v := got.Get("Accept-Encoding"); v == fixture.Headers["Accept-Encoding"] {
		t.Errorf("Accept-Encoding = %q, want the transport's", v)
	}
}
