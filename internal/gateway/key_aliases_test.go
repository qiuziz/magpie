package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

func TestAliasBodyRoutesARawRequestWithoutRewritingIt(t *testing.T) {
	plain := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"OK"}]}`)
	wire := gzipped(t, plain)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wire))
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(provider.RawHeader, "1")
	who := access.Identity{Aliases: map[string]string{"deepseek-v4.1-flash": "group/codebuddy"}}
	r = aliasBody(r, who)
	if got := routeModelOf(r.Context()); got != "group/codebuddy" {
		t.Fatalf("routeModelOf = %q, want group/codebuddy", got)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wire) {
		t.Errorf("body = %d bytes, want the client's own %d gzip bytes", len(got), len(wire))
	}
}

func TestAliasBodyRewritesAnOrdinaryRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"deepseek-v4.1-flash"}`)))
	r.Header.Set("Content-Type", "application/json")
	who := access.Identity{Aliases: map[string]string{"deepseek-v4.1-flash": "group/codebuddy"}}
	r = aliasBody(r, who)
	if got := routeModelOf(r.Context()); got != "" {
		t.Errorf("routeModelOf = %q, want none for an ordinary request", got)
	}
	got, _ := io.ReadAll(r.Body)
	if !bytes.Contains(got, []byte(`"group/codebuddy"`)) {
		t.Errorf("body = %q, want the model rewritten", got)
	}
}
