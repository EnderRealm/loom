// Package receiver implements the server side of the loom transport. It
// accepts POST /v1/ingest, appends each session's delta to a JSONL file
// under <storage>/<agent>/<project>/<session_id>.jsonl, and tracks per-
// session "next expected offset" so replays are idempotent.
//
// v1 is deliberately dumb: no database, no workers, no processing.
// Storage is the landing zone for downstream jobs (loom-summarize) to
// read at their leisure.
package receiver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"loom/internal/config"
	"loom/transport/internal/wire"
)

// AgentLabel is the launchd label for the receiver. Exposed so the install
// path can name its plist consistently.
const AgentLabel = "com.loom.receiver"

// Options configures Run. Zero values follow flag defaults: bind :8765,
// storage at $LOOM_HOME/received, auth from $LOOM_RECEIVER_TOKEN if Token
// is empty.
type Options struct {
	Addr    string // listen address (default ":8765")
	Storage string // storage root (default $LOOM_HOME/received)
	Token   string // bearer token; empty means look at LOOM_RECEIVER_TOKEN
}

// Run starts the receiver. Blocks until ctx is cancelled or the listener
// fails. Cancellation gracefully shuts down the HTTP server.
func Run(ctx context.Context, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = ":8765"
	}
	if opts.Storage == "" {
		opts.Storage = filepath.Join(config.Home(), "received")
	}
	if opts.Token == "" {
		opts.Token = os.Getenv("LOOM_RECEIVER_TOKEN")
	}
	if err := os.MkdirAll(opts.Storage, 0o700); err != nil {
		return fmt.Errorf("mkdir storage: %w", err)
	}

	srv := &server{
		storage:   opts.Storage,
		authToken: opts.Token,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ingest", srv.handleIngest)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	httpSrv := &http.Server{Addr: opts.Addr, Handler: mux}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("loom-receiver listening on %s storage=%s", opts.Addr, opts.Storage)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

const shutdownTimeout = 5 * time.Second

type server struct {
	storage   string
	authToken string

	// One global mutex for v1. Per-session locking is a later
	// optimization; under realistic load the writes are small and fast.
	mu sync.Mutex
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAuth(r) {
		log.Printf("reject 401 from=%s", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req wire.IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("reject 400 from=%s err=%q", r.RemoteAddr, err)
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Project == "" || req.SessionID == "" {
		log.Printf("reject 400 from=%s reason=missing-fields", r.RemoteAddr)
		http.Error(w, "missing required fields", http.StatusBadRequest)
		return
	}
	if !safeComponent(req.Agent) || !safeComponent(req.Project) || !safeComponent(req.SessionID) {
		log.Printf("reject 400 from=%s reason=invalid-identifier", r.RemoteAddr)
		http.Error(w, "invalid identifier", http.StatusBadRequest)
		return
	}
	if req.ToOffset < req.FromOffset {
		log.Printf("reject 400 from=%s reason=bad-offsets", r.RemoteAddr)
		http.Error(w, "to_offset must be >= from_offset", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.storage, req.Agent, req.Project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sessionPath := filepath.Join(dir, req.SessionID+".jsonl")
	offsetPath := filepath.Join(dir, req.SessionID+".offset")

	currentNext, err := readOffsetFile(offsetPath)
	if err != nil {
		http.Error(w, "read offset: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Integrity check: refuse if the data file is shorter than the offset
	// claims. Manual repair is preferable to silently accepting a replay
	// that covers a gap.
	if currentNext > 0 {
		fi, statErr := os.Stat(sessionPath)
		if statErr != nil && !os.IsNotExist(statErr) {
			http.Error(w, "stat session: "+statErr.Error(), http.StatusInternalServerError)
			return
		}
		dataSize := int64(0)
		if fi != nil {
			dataSize = fi.Size()
		}
		if dataSize < currentNext {
			log.Printf("corruption from=%s session=%s offset=%d data_size=%d (offset ahead of data)",
				r.RemoteAddr, req.SessionID, currentNext, dataSize)
			http.Error(w,
				fmt.Sprintf("offset/data mismatch: offset=%d but data file is %d bytes — manual repair needed", currentNext, dataSize),
				http.StatusInternalServerError)
			return
		}
	}

	if req.FromOffset < currentNext {
		if req.ToOffset <= currentNext {
			log.Printf("replay from=%s agent=%s project=%s session=%s (no-op, already at %d)",
				r.RemoteAddr, req.Agent, req.Project, req.SessionID, currentNext)
			writeJSON(w, wire.IngestResponse{AcceptedToOffset: currentNext})
			return
		}
		log.Printf("reject 409 from=%s session=%s reason=partial-replay expected=%d got=%d→%d",
			r.RemoteAddr, req.SessionID, currentNext, req.FromOffset, req.ToOffset)
		writeJSONStatus(w, http.StatusConflict, wire.IngestError{
			Error:        "partial replay not supported",
			ExpectedFrom: &currentNext,
		})
		return
	}

	if req.FromOffset > currentNext {
		log.Printf("reject 409 from=%s session=%s reason=gap expected=%d got=%d",
			r.RemoteAddr, req.SessionID, currentNext, req.FromOffset)
		writeJSONStatus(w, http.StatusConflict, wire.IngestError{
			Error:        "gap: server expected lower from_offset",
			ExpectedFrom: &currentNext,
		})
		return
	}

	// In-order: append lines, then advance the offset. If we crash
	// between the two, the next retry replays the same lines and we'd
	// rather have duplicates than missing bytes.
	f, err := os.OpenFile(sessionPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		http.Error(w, "open session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, l := range req.Lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			f.Close()
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		http.Error(w, "fsync: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		http.Error(w, "close: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeOffsetFile(offsetPath, req.ToOffset); err != nil {
		log.Printf("error from=%s session=%s err=%q (write offset)", r.RemoteAddr, req.SessionID, err)
		http.Error(w, "write offset: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist the per-session identity sidecar when the client supplied
	// it. Missing field is fine: pre-identity clients ship under the
	// legacy slug-only shape and downstream readers fall back to the
	// directory name. Failures here don't block ingest — the raw bytes
	// are already on disk.
	identity := "none"
	if req.ProjectIdentity != nil && *req.ProjectIdentity != (wire.ProjectIdentity{}) {
		metaPath := filepath.Join(dir, req.SessionID+".meta.json")
		if err := writeProjectIdentity(metaPath, req.ProjectIdentity); err != nil {
			log.Printf("warn from=%s session=%s err=%q (write meta)", r.RemoteAddr, req.SessionID, err)
		}
		switch {
		case req.ProjectIdentity.GitRemote != "":
			identity = "remote=" + req.ProjectIdentity.GitRemote
		case req.ProjectIdentity.Cwd != "":
			identity = "cwd=" + req.ProjectIdentity.Cwd
		default:
			identity = "slug-only"
		}
	}

	log.Printf("ingest from=%s agent=%s project=%s session=%s offset=%d→%d lines=%d identity=%s",
		r.RemoteAddr, req.Agent, req.Project, req.SessionID, req.FromOffset, req.ToOffset, len(req.Lines), identity)
	writeJSON(w, wire.IngestResponse{AcceptedToOffset: req.ToOffset})
}

// writeProjectIdentity writes the sidecar atomically via temp+rename so
// a partial write never leaves a corrupt JSON file behind.
func writeProjectIdentity(path string, id *wire.ProjectIdentity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *server) checkAuth(r *http.Request) bool {
	if s.authToken == "" {
		// No token configured — allow (useful for localhost dev).
		return true
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.authToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func readOffsetFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return n, nil
}

func writeOffsetFile(path string, offset int64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func safeComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, "/\\") {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
