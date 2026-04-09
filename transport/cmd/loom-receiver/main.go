// loom-receiver is the minimal ingest endpoint for agent session deltas.
//
// It accepts POST /v1/ingest with a wire.IngestRequest body, appends the
// contained lines to <storage>/<agent>/<project>/<session_id>.jsonl, and
// tracks per-session "next expected offset" so replays are idempotent.
//
// v1 is deliberately dumb: no database, no workers, no processing. Storage
// is the landing zone for downstream jobs to read at their leisure.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"loom/internal/config"
	"loom/transport/internal/wire"
)

var (
	addr      = flag.String("addr", ":8765", "listen address")
	storage   = flag.String("storage", "", "storage directory for ingested sessions (default: $LOOM_HOME/received or ~/.loom/received)")
	authToken = flag.String("auth-token", "", "shared bearer token required on /v1/ingest (env: LOOM_RECEIVER_TOKEN)")

	// One global mutex for v1. Per-session locking is a later optimization.
	mu sync.Mutex
)

func main() {
	flag.Parse()
	if *authToken == "" {
		*authToken = os.Getenv("LOOM_RECEIVER_TOKEN")
	}
	if *storage == "" {
		*storage = filepath.Join(config.Home(), "received")
	}
	if err := os.MkdirAll(*storage, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir storage:", err)
		os.Exit(1)
	}

	http.HandleFunc("/v1/ingest", handleIngest)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	log.Printf("loom-receiver listening on %s storage=%s", *addr, *storage)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
}

func handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkAuth(r) {
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

	mu.Lock()
	defer mu.Unlock()

	dir := filepath.Join(*storage, req.Agent, req.Project)
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

	// Replay: client is sending data we've already accepted.
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
			ExpectedFrom: currentNext,
		})
		return
	}

	// Gap: client has advanced past the server (server lost state?).
	if req.FromOffset > currentNext {
		log.Printf("reject 409 from=%s session=%s reason=gap expected=%d got=%d",
			r.RemoteAddr, req.SessionID, currentNext, req.FromOffset)
		writeJSONStatus(w, http.StatusConflict, wire.IngestError{
			Error:        "gap: server expected lower from_offset",
			ExpectedFrom: currentNext,
		})
		return
	}

	// In-order: append lines, then advance the offset.
	// Order matters: if we crash between the two, the next retry will replay
	// the same lines and we'd rather have duplicates than missing bytes.
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
	if err := f.Close(); err != nil {
		http.Error(w, "close: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeOffsetFile(offsetPath, req.ToOffset); err != nil {
		log.Printf("error from=%s session=%s err=%q (write offset)", r.RemoteAddr, req.SessionID, err)
		http.Error(w, "write offset: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("ingest from=%s agent=%s project=%s session=%s offset=%d→%d lines=%d",
		r.RemoteAddr, req.Agent, req.Project, req.SessionID, req.FromOffset, req.ToOffset, len(req.Lines))
	writeJSON(w, wire.IngestResponse{AcceptedToOffset: req.ToOffset})
}

// ---------- helpers ----------

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

func checkAuth(r *http.Request) bool {
	if *authToken == "" {
		// No token configured — allow (useful for localhost dev).
		return true
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + *authToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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
