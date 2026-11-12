package gateway

// Raw passthrough: a client's own request goes to a provider behind a
// plugin as the client sent it — its bytes, its User-Agent and its other
// headers — with only the account's binding headers set (provider.Raw).
// The plugin's fetch does the signing (the WorkBuddy plugin's raw switch),
// so the vendor sees the client's own fingerprint; the body is never
// parsed, fitted, translated or gzipped on this path.
//
// It is native's shape (native.go) without native's keyed-only rule: the
// provider is a plugin's account, not an API key, and the route is the
// client's own /v1/chat/completions rather than /v1/magpie/native.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// rawRequest is the client's request as it came, kept before the gateway
// translates it: the bytes it sent and the headers it sent them with.
type rawRequest struct {
	body   []byte
	header http.Header
}

type rawKey struct{}

func withRawRequest(ctx context.Context, raw *rawRequest) context.Context {
	return context.WithValue(ctx, rawKey{}, raw)
}

func rawRequestOf(ctx context.Context) *rawRequest {
	raw, _ := ctx.Value(rawKey{}).(*rawRequest)
	return raw
}

// rawHeaderDropped are the headers a raw request doesn't take from the
// client: the hop's own, the ones Go's client sets from the URL and the
// body, and the credential the plugin signs with. User-Agent stays: a raw
// request goes as the client sent it.
func rawHeaderDropped(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Host", "Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Content-Length", "Accept-Encoding",
		"Authorization", "Proxy-Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Cookie":
		return true
	}
	return strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Magpie-")
}

// rawForward sends the client's own bytes to p and passes the answer back
// as it came. Only the account is magpie's: the plugin's fetch sets the
// three headers binding it, and the body, User-Agent and every other
// client header go untouched.
func (s *Server) rawForward(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, raw *rawRequest, call *Call) (int, string) {
	start := call.Time
	ctx := s.metered(p.Via(r.Context()), p, "")
	upstream := provider.UpstreamNameIn(wiresOf(ctx), p.ID, model)
	body := rewriteModel(raw.body, upstream)
	path := upstreamPath(p, provider.Chat, upstream)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(provider.Chat)+path, bytes.NewReader(body))
	if err != nil {
		return writeError(w, provider.Chat, 502, p.Name+": "+err.Error()), err.Error()
	}
	for k, vs := range raw.header {
		if rawHeaderDropped(k) {
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	// the headers the user gave the provider have the last word: a raw
	// provider's account binding rides in them (a plugin's fetch sets its
	// own instead)
	for k, v := range p.SentHeaders(ctx) {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	// the plugin's fetch is told to send as raw: its loader deletes this
	// before the request goes on, and only the account's three headers are
	// set (the plugin's own raw switch makes the same choice). A provider
	// without a plugin goes straight to its own host, so it isn't told.
	ctx = provider.WithModel(ctx, p.ID, model)
	req = req.WithContext(ctx)
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return writeError(w, provider.Chat, 502, p.Name+": "+err.Error()), err.Error()
	}
	if p.IsPlugin() {
		req.Header.Set(provider.RawHeader, "1")
	}
	call.To = provider.Chat
	res, err := p.Do(s.client, req)
	if err != nil {
		return writeError(w, provider.Chat, 502, fmt.Sprintf("%s didn't answer: %v", p.Name, err)), err.Error()
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		if nativeAnswerDropped[http.CanonicalHeaderKey(k)] || (res.Uncompressed && (k == "Content-Length" || k == "Content-Encoding")) {
			continue
		}
		w.Header()[k] = append([]string(nil), vs...)
	}
	w.WriteHeader(res.StatusCode)
	ct := res.Header.Get("Content-Type")
	var sn *usageSniffer
	if strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/event-stream") {
		sn = newSniffer(provider.Chat, ct)
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if sn != nil {
				sn.write(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			rc.Flush()
		}
		if err != nil {
			break
		}
	}
	call.Status, call.Millis = res.StatusCode, time.Since(start).Milliseconds()
	if res.StatusCode >= 400 {
		call.Error = fmt.Sprintf("%s answered %d", p.Name, res.StatusCode)
	}
	var u Usage
	if sn != nil && res.StatusCode < 400 {
		u = sn.usage()
	}
	call.Usage = u
	s.record(*call)
	appendUsage(r, usage.Record{Operation: "raw", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: call.Model, Requested: call.Model, Served: u.Served, Upstream: u.Upstream,
		ProviderKeyID: provider.KeyID(p.Key), ProviderKeyName: p.KeyName,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, CacheWrite1h: u.CacheWrite1h, Reasoning: u.Reasoning,
		Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
	return res.StatusCode, ""
}
