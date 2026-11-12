package gateway

import (
	"bytes"
	"compress/gzip"
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

func TestRawForwardKeepsTheClientsHeaders(t *testing.T) {
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
	if len(fixture.Headers) != 41 {
		t.Fatalf("fixture has %d headers, want 41", len(fixture.Headers))
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
	changed := map[string]bool{"Authorization": true, "Content-Length": true, "Host": true, "Connection": true, "X-Api-Key": true, "X-User-Id": true, "X-Domain": true}
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
}

// gzipped is body as CBC sends it: its chat body arrives gzip-compressed,
// Content-Encoding naming the encoding.
func gzipped(t *testing.T, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRawForwardKeepsTheClientsCompressedBytes(t *testing.T) {
	var gotBody []byte
	var gotEnc string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotEnc = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	p := provider.Provider{ID: "raw-gzip", Name: "RawGzip", Chat: upstream.URL, Key: "key", Raw: true,
		Headers: map[string]string{"X-User-Id": "uid-account", "X-Domain": "copilot.tencent.com"}}
	s := &Server{client: upstream.Client()}
	plain := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"OK"}]}`)
	wire := gzipped(t, plain)
	raw := &rawRequest{body: wire, header: http.Header{
		"Content-Encoding": {"gzip"},
		"Content-Type":     {"application/json"},
		"User-Agent":       {"CLI/2.180.0 CodeBuddy/2.180.0"},
	}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(withRawRequest(r.Context(), raw))
	status, msg := s.rawForward(rec, r, p, "deepseek-v4.1-flash", raw, &Call{Time: time.Now(), Model: "deepseek-v4.1-flash"})
	if status != http.StatusOK || msg != "" {
		t.Fatalf("rawForward = %d %q, want 200", status, msg)
	}
	if !bytes.Equal(gotBody, wire) {
		t.Errorf("body = %d bytes, want the client's own %d gzip bytes", len(gotBody), len(wire))
	}
	if gotEnc != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", gotEnc)
	}
}

func TestRawRequestBodyKeepsTheEncoding(t *testing.T) {
	plain := []byte(`{"model":"deepseek-v4.1-flash"}`)
	wire := gzipped(t, plain)
	r := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", bytes.NewReader(wire))
	r.Header.Set("Content-Encoding", "gzip")
	r.ContentLength = int64(len(wire))
	rec := httptest.NewRecorder()
	body, status, err := readBoundedRequestKind(rec, r, requestLimits{}, true)
	if err != nil || status != 0 {
		t.Fatalf("readBoundedRequestKind = %d %v", status, err)
	}
	if !bytes.Equal(body, wire) {
		t.Errorf("body = %d bytes, want the wire's %d", len(body), len(wire))
	}
	if enc := r.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip still set", enc)
	}
	decoded, err := decodedCopy(body, r.Header, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plain) {
		t.Errorf("decodedCopy = %q, want %q", decoded, plain)
	}
}

func TestRawAsked(t *testing.T) {
	if rawAsked(http.Header{}) {
		t.Error("rawAsked(no header) = true")
	}
	if !rawAsked(http.Header{provider.RawHeader: {"1"}}) {
		t.Error("rawAsked(X-Magpie-Raw: 1) = false")
	}
}
