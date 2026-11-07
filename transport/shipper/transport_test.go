package shipper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"loom/transport/internal/staging"
	"loom/transport/internal/wire"
)

// tlsHost is a name the httptest certificate is valid for. Plain and TLS
// listeners both answer for it, so a redirect between them is same-host,
// which is the case where net/http keeps the Authorization header.
const (
	tlsHost   = "example.com"
	testToken = "s3cret-bearer-token"
	testLine  = `{"secret":"transcript-body"}`
)

type capture struct {
	mu   sync.Mutex
	hits int
	seen string
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits++
	c.seen += r.Header.Get("Authorization") + string(body)
	w.Write([]byte(`{}`))
}

func (c *capture) snapshot() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.seen
}

func port(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// routeToServers makes the default client trust tlsSrv and dial tlsHost to
// the listener owning the requested port. No DNS is involved, and a request
// that escapes the policy really lands on a listener a test can inspect.
func routeToServers(t *testing.T, tlsSrv *httptest.Server, servers ...*httptest.Server) {
	t.Helper()
	listeners := map[string]string{port(t, tlsSrv): tlsSrv.Listener.Addr().String()}
	for _, s := range servers {
		listeners[port(t, s)] = s.Listener.Addr().String()
	}
	transport := tlsSrv.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, p, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, network, listeners[p])
	}
	orig := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = orig })
}

func ingestRequest() wire.IngestRequest {
	return wire.IngestRequest{Agent: "claude", Project: "p", SessionID: "s", ToOffset: int64(len(testLine) + 1), Lines: []string{testLine}}
}

// An https receiver redirecting to http must not carry the token or the
// transcript across in the clear. The opted-in case is the control: the same
// redirect does reach the plaintext listener with both when the operator has
// accepted that, so a refusal here is the check working and not a harness
// that could not have leaked.
func TestRedirectToPlaintextIsRefused(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		for _, optIn := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d optIn=%t", code, optIn), func(t *testing.T) {
				plain := &capture{}
				plainSrv := httptest.NewServer(plain)
				defer plainSrv.Close()
				var redirected sync.Map
				tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					redirected.Store(r.URL.Path, true)
					http.Redirect(w, r, fmt.Sprintf("http://%s:%s%s", tlsHost, port(t, plainSrv), r.URL.Path), code)
				}))
				defer tlsSrv.Close()
				routeToServers(t, tlsSrv, plainSrv)
				writeConfig(t, fmt.Sprintf(`{"server_url":"https://%s:%s","auth_token":%q,"allow_plaintext_transcripts_and_token":%t}`,
					tlsHost, port(t, tlsSrv), testToken, optIn))
				cfg, err := LoadConfig()
				if err != nil {
					t.Fatal(err)
				}

				healthErr := healthCheck(cfg)
				_, ingestErr := postIngestWithRetry(cfg, ingestRequest(), staging.Entry{})
				hits, seen := plain.snapshot()

				if optIn {
					if healthErr != nil || hits != 2 || !strings.Contains(seen, testToken) {
						t.Fatalf("control: health=%v hits=%d seen=%q", healthErr, hits, seen)
					}
					if code != http.StatusFound && !strings.Contains(seen, "transcript-body") {
						t.Fatalf("control: body not re-sent, seen=%q", seen)
					}
					return
				}
				if hits != 0 {
					t.Fatalf("plaintext listener got %d requests: %q", hits, seen)
				}
				for name, err := range map[string]error{"health": healthErr, "ingest": ingestErr} {
					if !errors.Is(err, ErrTransportRefused) || classify(err) != classPolicy {
						t.Errorf("%s err = %v (class %q), want a %s refusal", name, err, classify(err), classPolicy)
					}
					if strings.Contains(err.Error(), testToken) {
						t.Errorf("%s err carries the token: %v", name, err)
					}
				}
				for _, path := range []string{"/healthz", "/v1/ingest"} {
					if _, ok := redirected.Load(path); !ok {
						t.Errorf("%s never reached the https server", path)
					}
				}
			})
		}
	}
}

// A refusal is final, so the ingest path spends no retries re-asking for it.
func TestRefusedRedirectIsNotRetried(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Redirect(w, r, "http://"+tlsHost+":1/v1/ingest", http.StatusTemporaryRedirect)
	}))
	defer tlsSrv.Close()
	routeToServers(t, tlsSrv)
	cfg := &Config{ServerURL: fmt.Sprintf("https://%s:%s", tlsHost, port(t, tlsSrv))}

	if _, err := postIngestWithRetry(cfg, ingestRequest(), staging.Entry{}); !errors.Is(err, ErrTransportRefused) {
		t.Fatalf("err = %v", err)
	}
	if hits != 1 {
		t.Fatalf("server saw %d attempts, want 1", hits)
	}
}

// Redirects the policy allows keep working, and a chain ending at a refused
// hop is judged at that hop. Setting CheckRedirect drops net/http's default
// limit, so a loop must still stop.
func TestRedirectPolicyAcrossHops(t *testing.T) {
	var tlsSrv *httptest.Server
	tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok":
		case "/loop/healthz":
			http.Redirect(w, r, "/loop/healthz", http.StatusFound)
		case "/chain/healthz":
			http.Redirect(w, r, "/chain/next", http.StatusFound)
		case "/chain/next":
			http.Redirect(w, r, "http://"+tlsHost+":1/x", http.StatusFound)
		}
	}))
	defer tlsSrv.Close()
	routeToServers(t, tlsSrv)
	base := fmt.Sprintf("https://%s:%s", tlsHost, port(t, tlsSrv))

	if err := healthCheck(&Config{ServerURL: base}); err != nil {
		t.Fatalf("https redirect: %v", err)
	}
	if err := healthCheck(&Config{ServerURL: base + "/chain"}); !errors.Is(err, ErrTransportRefused) {
		t.Fatalf("chain: %v", err)
	}
	if err := healthCheck(&Config{ServerURL: base + "/loop"}); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("loop: %v", err)
	}
}

// The token and transcript reach an https receiver over a verified
// connection; the same receiver is unreachable to a client that does not
// trust its certificate, which is what makes the https requirement mean
// anything.
func TestShipsOverVerifiedTLS(t *testing.T) {
	received := &capture{}
	tlsSrv := httptest.NewTLSServer(received)
	defer tlsSrv.Close()
	writeConfig(t, fmt.Sprintf(`{"server_url":%q,"auth_token":%q}`, tlsSrv.URL, testToken))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if err := healthCheck(cfg); err == nil {
		t.Fatal("health check accepted an untrusted certificate")
	}
	if hits, _ := received.snapshot(); hits != 0 {
		t.Fatalf("untrusted handshake reached the handler %d times", hits)
	}

	routeToServers(t, tlsSrv)
	if err := healthCheck(cfg); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if _, err := postIngest(cfg, ingestRequest()); err != nil {
		t.Fatalf("postIngest: %v", err)
	}
	if _, seen := received.snapshot(); !strings.Contains(seen, "Bearer "+testToken) || !strings.Contains(seen, "transcript-body") {
		t.Fatalf("receiver saw %q", seen)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &logs
}

// A tick against a refused server_url logs why and sends nothing.
func TestOnceRefusesPlaintextServerURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	received := &capture{}
	srv := httptest.NewServer(received)
	defer srv.Close()
	tlsSrv := httptest.NewTLSServer(received)
	defer tlsSrv.Close()
	routeToServers(t, tlsSrv, srv)
	writeConfig(t, fmt.Sprintf(`{"server_url":"http://%s:%s","auth_token":%q,"notify_on_failure":false}`, tlsHost, port(t, srv), testToken))
	logs := captureLog(t)

	Once()

	if hits, seen := received.snapshot(); hits != 0 {
		t.Fatalf("receiver got %d requests: %q", hits, seen)
	}
	if !strings.Contains(logs.String(), "fail stage=config") || !strings.Contains(logs.String(), allowPlaintextKey) {
		t.Fatalf("log = %q", logs)
	}
}

// The opt-out ships, and every tick says it is on.
func TestOnceWarnsUnderTheOptOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	received := &capture{}
	srv := httptest.NewServer(received)
	defer srv.Close()
	tlsSrv := httptest.NewTLSServer(received)
	defer tlsSrv.Close()
	routeToServers(t, tlsSrv, srv)
	writeConfig(t, fmt.Sprintf(`{"server_url":"http://%s:%s","allow_plaintext_transcripts_and_token":true,"notify_on_failure":false}`, tlsHost, port(t, srv)))
	logs := captureLog(t)

	Once()

	if hits, _ := received.snapshot(); hits == 0 {
		t.Fatal("opted-in tick never reached the receiver")
	}
	if !strings.Contains(logs.String(), "warn stage=config "+allowPlaintextKey) {
		t.Fatalf("log = %q", logs)
	}
}
