// Package shipper drives the client side of the loom transport: it walks
// agent session files, stages new bytes locally, and ships staged deltas to
// the loom-receiver. Two-stage flow:
//
//  1. Capture: append new source bytes into ~/.loom/transport/staging so we
//     survive the agent cleaning up its own session files.
//  2. Ship: health-check the receiver, then post each staged session's delta
//     with retry+backoff and classified error logging.
//
// Both Once (one tick) and Daemon (in-process ticker) live here; the cobra
// surface in cmd/loom/cmd/shipper.go is a thin wrapper.
package shipper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"loom/internal/config"
	"loom/transport/internal/cursor"
	"loom/transport/internal/notify"
	"loom/transport/internal/source"
	"loom/transport/internal/staging"
	"loom/transport/internal/wire"
)

// ---------- error classification ----------

type errClass string

const (
	classNone   errClass = ""
	classNet    errClass = "net"    // dial/timeout/EOF — retryable
	class5xx    errClass = "5xx"    // server error — retryable
	class4xx    errClass = "4xx"    // client error — NOT retryable
	classResync errClass = "resync" // 409 — handled by cursor correction
	classIO     errClass = "io"     // local file/cursor error
)

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("status %d: %s", e.status, e.body)
}

func classify(err error) errClass {
	if err == nil {
		return classNone
	}
	var re *wire.ResyncError
	if errors.As(err, &re) {
		return classResync
	}
	var he *httpError
	if errors.As(err, &he) {
		switch {
		case he.status >= 500:
			return class5xx
		case he.status >= 400:
			return class4xx
		}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return classNet
	}
	if isNetError(err) {
		return classNet
	}
	return classIO
}

func isNetError(err error) bool {
	msg := err.Error()
	for _, s := range []string{"connection refused", "no such host", "EOF", "broken pipe", "i/o timeout", "connect: "} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// ---------- once ----------

type tickCounts struct {
	captured      int
	captureFailed int
	shipped       int
	skipped       int
	failed        int
	byClass       map[errClass]int
}

func (t *tickCounts) addFail(c errClass) {
	t.failed++
	if t.byClass == nil {
		t.byClass = map[errClass]int{}
	}
	t.byClass[c]++
}

func (t tickCounts) classBreakdown() string {
	if len(t.byClass) == 0 {
		return ""
	}
	parts := []string{}
	for _, c := range []errClass{classNet, class5xx, class4xx, classIO} {
		if n := t.byClass[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", c, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// Once executes one capture+ship pass. Acquires the file lock so a
// concurrent Daemon and a manual `loom shipper once` don't race on
// staging or cursors. Errors are logged but not returned: the caller is
// the launchd job, and a non-zero exit there triggers KeepAlive respawn
// which we don't want for a normal failed tick.
func Once() {
	if err := acquireLock(); err != nil {
		fmt.Fprintln(os.Stderr, "another shipper is running, skipping")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Printf("fail stage=config err=%q", err)
		return
	}

	if err := cursor.Migrate(); err != nil {
		log.Printf("fail stage=migrate err=%q", err)
	}

	state, err := notify.LoadState()
	if err != nil {
		log.Printf("fail stage=notify-load err=%q", err)
		state = &notify.State{PendingSessions: map[string]bool{}}
	}

	counts := tickCounts{}
	now := time.Now().UTC()

	capturePass(&counts)

	healthErr := healthCheck(cfg)
	var shipSummary string
	if healthErr != nil {
		hClass := classify(healthErr)
		log.Printf("fail stage=healthcheck class=%s err=%q", hClass, healthErr)
		refreshPending(state)
		maybeNotify(cfg, state, notify.Event{
			Kind:    notify.KindReceiverDown,
			Title:   "loom-shipper: receiver unavailable",
			Summary: fmt.Sprintf("Receiver unreachable (%s)", hClass),
			Now:     now,
		})
		shipSummary = "ship-skipped"
	} else {
		state.LastSuccessTS = now

		shipPass(cfg, &counts)
		refreshPending(state)
		shipSummary = fmt.Sprintf("shipped=%d skipped=%d failed=%d%s",
			counts.shipped, counts.skipped, counts.failed, counts.classBreakdown())

		if counts.failed > 0 {
			maybeNotify(cfg, state, notify.Event{
				Kind:    notify.KindShipFailed,
				Title:   "loom-shipper: ship failures",
				Summary: fmt.Sprintf("%d session%s failed to ship (%s)", counts.failed, pluralS(counts.failed), classSummary(counts.byClass)),
				Now:     now,
			})
		} else if counts.captureFailed == 0 {
			maybeNotify(cfg, state, notify.Event{
				Kind:    notify.KindRecovered,
				Title:   "loom-shipper: recovered",
				Summary: "Shipping is healthy again",
				Now:     now,
			})
		}
	}

	if counts.captureFailed > 0 {
		maybeNotify(cfg, state, notify.Event{
			Kind:    notify.KindLocalError,
			Title:   "loom-shipper: local error",
			Summary: fmt.Sprintf("%d session%s failed to capture", counts.captureFailed, pluralS(counts.captureFailed)),
			Now:     now,
		})
	}

	if err := state.Save(); err != nil {
		log.Printf("fail stage=notify-save err=%q", err)
	}

	log.Printf("done captured=%d capture-failed=%d %s pending=%d",
		counts.captured, counts.captureFailed, shipSummary, len(state.PendingSessions))
}

// Daemon runs Once on a ticker driven by config.IntervalMinutes (or
// DefaultIntervalMinutes when unset). Designed to run as a launchd
// KeepAlive job so the kernel respawns us on crash; we sleep between
// ticks ourselves rather than relying on launchd's StartInterval, which
// dasd coalesces aggressively.
//
// Cancellable via SIGINT / SIGTERM through the supplied context.
func Daemon(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	interval := cfg.IntervalMinutes
	if interval <= 0 {
		interval = config.DefaultIntervalMinutes
	}
	d := time.Duration(interval) * time.Minute
	log.Printf("daemon starting interval=%s", d)

	// Run once immediately so the agent gets fresh data on launchd's first
	// spawn, then settle into the ticker cadence.
	Once()

	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("daemon shutdown requested")
			return nil
		case <-t.C:
			Once()
		}
	}
}

// ---------- capture pass ----------

func capturePass(counts *tickCounts) {
	// One git-remote resolution per cwd per tick. Saves a fork+exec per
	// session when many sessions share the same repo.
	gitCache := map[string]string{}

	for _, ad := range source.Adapters() {
		agent := ad.Agent()
		sessions, err := ad.List()
		if err != nil {
			log.Printf("fail stage=capture agent=%s class=io err=%q", agent, err)
			counts.captureFailed++
			continue
		}
		for _, s := range sessions {
			from, err := cursor.Read(cursor.KindSource, agent, s.SessionID)
			if err != nil {
				log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q",
					agent, s.Project, s.SessionID, err)
				counts.captureFailed++
				continue
			}
			data, to, err := source.ReadDeltaBytes(s.Path, from)
			if err != nil {
				log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q",
					agent, s.Project, s.SessionID, err)
				counts.captureFailed++
				continue
			}
			if len(data) == 0 {
				continue
			}
			if err := staging.Append(agent, s.Project, s.SessionID, data); err != nil {
				log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q (append)",
					agent, s.Project, s.SessionID, err)
				counts.captureFailed++
				continue
			}
			// Refresh the identity sidecar on every successful capture
			// so receiver and downstream readers see the latest cwd /
			// git remote even if the working tree was moved.
			if s.Cwd != "" {
				remote, ok := gitCache[s.Cwd]
				if !ok {
					remote = resolveGitRemote(s.Cwd)
					gitCache[s.Cwd] = remote
				}
				if err := staging.WriteIdentity(agent, s.Project, s.SessionID, staging.Identity{
					GitRemote: remote,
					Cwd:       s.Cwd,
					RootSlug:  s.Project,
				}); err != nil {
					log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q (meta)",
						agent, s.Project, s.SessionID, err)
				}
			}
			if err := cursor.Write(cursor.KindSource, agent, s.SessionID, to); err != nil {
				log.Printf("fail stage=capture agent=%s project=%s session=%s class=io err=%q (cursor)",
					agent, s.Project, s.SessionID, err)
				counts.captureFailed++
				continue
			}
			log.Printf("capture agent=%s project=%s session=%s bytes=%d offset=%d→%d",
				agent, s.Project, s.SessionID, len(data), from, to)
			counts.captured++
		}
	}
}

// resolveGitRemote runs `git -C cwd remote get-url origin`. Returns the
// trimmed URL on success, "" on any failure (no repo, no origin remote,
// git not installed, cwd doesn't exist). Best effort — the receiver
// persists whatever identity we send; missing remote just means project
// grouping falls back to cwd.
func resolveGitRemote(cwd string) string {
	if cwd == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", cwd, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---------- ship pass ----------

func shipPass(cfg *config.Config, counts *tickCounts) {
	agents, err := staging.AgentDirs()
	if err != nil {
		log.Printf("fail stage=ship class=io err=%q", err)
		counts.addFail(classIO)
		return
	}
	for _, agent := range agents {
		entries, err := staging.List(agent)
		if err != nil {
			log.Printf("fail stage=ship agent=%s class=io err=%q", agent, err)
			counts.addFail(classIO)
			continue
		}
		for _, e := range entries {
			shipOne(cfg, e, counts)
		}
	}
}

func shipOne(cfg *config.Config, e staging.Entry, counts *tickCounts) {
	from, err := cursor.Read(cursor.KindShip, e.Agent, e.SessionID)
	if err != nil {
		log.Printf("fail stage=ship agent=%s project=%s session=%s class=io err=%q (cursor read)",
			e.Agent, e.Project, e.SessionID, err)
		counts.addFail(classIO)
		return
	}
	lines, to, err := source.ReadDelta(e.Path, from)
	if err != nil {
		log.Printf("fail stage=ship agent=%s project=%s session=%s class=io err=%q (read staging)",
			e.Agent, e.Project, e.SessionID, err)
		counts.addFail(classIO)
		return
	}
	if len(lines) == 0 {
		counts.skipped++
		return
	}
	req := wire.IngestRequest{
		Agent:      e.Agent,
		Project:    e.Project,
		SessionID:  e.SessionID,
		FromOffset: from,
		ToOffset:   to,
		Lines:      lines,
	}
	if e.Identity != (staging.Identity{}) {
		req.ProjectIdentity = &wire.ProjectIdentity{
			GitRemote: e.Identity.GitRemote,
			Cwd:       e.Identity.Cwd,
			RootSlug:  e.Identity.RootSlug,
		}
	}
	accepted, err := postIngestWithRetry(cfg, req, e)
	if err != nil {
		class := classify(err)
		var resyncErr *wire.ResyncError
		if errors.As(err, &resyncErr) {
			log.Printf("resync agent=%s project=%s session=%s cursor=%d→%d (server expects %d: %s)",
				e.Agent, e.Project, e.SessionID, from, resyncErr.ExpectedFrom, resyncErr.ExpectedFrom, resyncErr.Detail)
			if wErr := cursor.Write(cursor.KindShip, e.Agent, e.SessionID, resyncErr.ExpectedFrom); wErr != nil {
				log.Printf("fail stage=ship agent=%s project=%s session=%s class=io err=%q (cursor resync write)",
					e.Agent, e.Project, e.SessionID, wErr)
				counts.addFail(classIO)
			}
			return
		}
		log.Printf("fail stage=ship agent=%s project=%s session=%s class=%s offset=%d→%d lines=%d err=%q",
			e.Agent, e.Project, e.SessionID, class, from, to, len(lines), err)
		counts.addFail(class)
		return
	}
	if err := cursor.Write(cursor.KindShip, e.Agent, e.SessionID, accepted); err != nil {
		log.Printf("fail stage=ship agent=%s project=%s session=%s class=io err=%q (cursor write)",
			e.Agent, e.Project, e.SessionID, err)
		counts.addFail(classIO)
		return
	}
	log.Printf("ship agent=%s project=%s session=%s offset=%d→%d lines=%d",
		e.Agent, e.Project, e.SessionID, from, accepted, len(lines))
	counts.shipped++
}

func postIngestWithRetry(cfg *config.Config, req wire.IngestRequest, e staging.Entry) (int64, error) {
	backoffs := []time.Duration{0, 500 * time.Millisecond, 2 * time.Second}
	var lastErr error
	for attempt := 0; attempt < len(backoffs); attempt++ {
		if d := jitter(backoffs[attempt]); d > 0 {
			time.Sleep(d)
		}
		accepted, err := postIngest(cfg, req)
		if err == nil {
			return accepted, nil
		}
		lastErr = err
		class := classify(err)
		if class != classNet && class != class5xx {
			return 0, err
		}
		if attempt+1 < len(backoffs) {
			log.Printf("retry agent=%s project=%s session=%s class=%s attempt=%d/%d err=%q",
				e.Agent, e.Project, e.SessionID, class, attempt+1, len(backoffs), err)
		}
	}
	return 0, lastErr
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 0.7 + rand.Float64()*0.6
	return time.Duration(float64(d) * factor)
}

func postIngest(cfg *config.Config, req wire.IngestRequest) (int64, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	url := strings.TrimRight(cfg.ServerURL, "/") + "/v1/ingest"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		var ie wire.IngestError
		if err := json.NewDecoder(resp.Body).Decode(&ie); err == nil && ie.ExpectedFrom != nil {
			return 0, &wire.ResyncError{
				ExpectedFrom: *ie.ExpectedFrom,
				Detail:       ie.Error,
			}
		}
		b, _ := io.ReadAll(resp.Body)
		return 0, &httpError{status: 409, body: strings.TrimSpace(string(b))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	var out wire.IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return out.AcceptedToOffset, nil
}

func healthCheck(cfg *config.Config) error {
	url := strings.TrimRight(cfg.ServerURL, "/") + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return nil
}

// ---------- notifier helpers ----------

func refreshPending(state *notify.State) {
	pending := map[string]bool{}
	agents, err := staging.AgentDirs()
	if err != nil {
		return
	}
	for _, agent := range agents {
		sessions, err := cursor.ListSessions(cursor.KindShip, agent)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, sid := range sessions {
			seen[sid] = true
		}
		entries, err := staging.List(agent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			seen[e.SessionID] = true
		}
		for sid := range seen {
			var ent *staging.Entry
			for i := range entries {
				if entries[i].SessionID == sid {
					ent = &entries[i]
					break
				}
			}
			if ent == nil {
				continue
			}
			size, err := staging.Size(agent, ent.Project, sid)
			if err != nil {
				continue
			}
			shipOff, _ := cursor.Read(cursor.KindShip, agent, sid)
			if shipOff < size {
				pending[notify.SessionKey(agent, sid)] = true
			}
		}
	}
	state.PendingSessions = pending
}

func maybeNotify(cfg *config.Config, state *notify.State, ev notify.Event) {
	emitted, err := state.Maybe(cfg, ev)
	if err != nil {
		log.Printf("fail stage=notify kind=%s err=%q", ev.Kind, err)
		return
	}
	if emitted {
		log.Printf("notify kind=%s title=%q", ev.Kind, ev.Title)
	}
}

func classSummary(byClass map[errClass]int) string {
	if len(byClass) == 0 {
		return "unknown"
	}
	parts := []string{}
	for _, c := range []errClass{classNet, class5xx, class4xx, classIO} {
		if n := byClass[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", c, n))
		}
	}
	return strings.Join(parts, " ")
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------- lock ----------

// AgentLabel is the launchd label for the shipper. Exposed so the install
// path can name its plist consistently with what Daemon expects.
const AgentLabel = "com.loom.shipper"

func lockPath() string {
	return filepath.Join(config.TransportDir(), "shipper.lock")
}

func acquireLock() error {
	if err := os.MkdirAll(filepath.Dir(lockPath()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// Non-blocking exclusive lock. f is intentionally leaked: the lock releases
	// automatically when this process exits.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return err
	}
	return nil
}

// ---------- health printer ----------

// PrintHealth surfaces the same info carried in failure notifications: when
// the shipper last succeeded, how many sessions are unsynced, and which
// kind of failure (if any) is currently outstanding. `loom status` calls it.
func PrintHealth(w io.Writer) error {
	state, err := notify.LoadState()
	if err != nil {
		return fmt.Errorf("read notify state: %w", err)
	}
	now := time.Now().UTC()

	if state.LastSuccessTS.IsZero() {
		fmt.Fprintln(w, "  last successful sync: never")
	} else {
		fmt.Fprintf(w, "  last successful sync: %s (%s ago)\n",
			state.LastSuccessTS.Local().Format("2006-01-02 15:04:05 MST"),
			notify.FormatDuration(now.Sub(state.LastSuccessTS)))
	}

	pending := len(state.PendingSessions)
	if pending == 0 {
		fmt.Fprintln(w, "  unshipped sessions:   0")
	} else {
		fmt.Fprintf(w, "  unshipped sessions:   %d\n", pending)
		printPendingByProject(w, state.PendingSessions)
	}

	uncap := countUncapturedSessions()
	if len(uncap) == 0 {
		fmt.Fprintln(w, "  uncaptured sessions:  0")
	} else {
		total := 0
		for _, c := range uncap {
			total += c.sessions
		}
		fmt.Fprintf(w, "  uncaptured sessions:  %d (%s not yet captured)\n",
			total, humanBytes(sumBytes(uncap)))
		sort.Slice(uncap, func(i, j int) bool {
			if uncap[i].agent != uncap[j].agent {
				return uncap[i].agent < uncap[j].agent
			}
			return uncap[i].project < uncap[j].project
		})
		for _, c := range uncap {
			fmt.Fprintf(w, "    %s / %s: %d session%s, %s\n",
				c.agent, c.project, c.sessions, pluralS(c.sessions), humanBytes(c.bytes))
		}
	}

	if state.LastNotifiedTS.IsZero() {
		fmt.Fprintln(w, "  last notification:    none")
	} else {
		fmt.Fprintf(w, "  last notification:    %s at %s (%s ago)\n",
			state.LastNotifiedKind,
			state.LastNotifiedTS.Local().Format("2006-01-02 15:04:05 MST"),
			notify.FormatDuration(now.Sub(state.LastNotifiedTS)))
	}

	if state.FailureActive {
		fmt.Fprintln(w, "  failure active:       yes (next healthy tick will notify recovered)")
	} else {
		fmt.Fprintln(w, "  failure active:       no")
	}
	return nil
}

type uncapturedBucket struct {
	agent    string
	project  string
	sessions int
	bytes    int64
}

func countUncapturedSessions() []uncapturedBucket {
	byKey := map[string]*uncapturedBucket{}
	for _, ad := range source.Adapters() {
		agent := ad.Agent()
		sessions, err := ad.List()
		if err != nil {
			continue
		}
		for _, s := range sessions {
			size, err := source.Size(s.Path)
			if err != nil {
				continue
			}
			off, _ := cursor.Read(cursor.KindSource, agent, s.SessionID)
			if size <= off {
				continue
			}
			k := agent + "\x00" + s.Project
			b, ok := byKey[k]
			if !ok {
				b = &uncapturedBucket{agent: agent, project: s.Project}
				byKey[k] = b
			}
			b.sessions++
			b.bytes += size - off
		}
	}
	out := make([]uncapturedBucket, 0, len(byKey))
	for _, b := range byKey {
		out = append(out, *b)
	}
	return out
}

func sumBytes(bs []uncapturedBucket) int64 {
	var n int64
	for _, b := range bs {
		n += b.bytes
	}
	return n
}

func humanBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func printPendingByProject(w io.Writer, pending map[string]bool) {
	type bucket struct {
		agent, project string
	}
	counts := map[bucket]int{}

	agents := map[string]bool{}
	for k := range pending {
		if i := strings.IndexByte(k, '/'); i > 0 {
			agents[k[:i]] = true
		}
	}

	projectOf := map[string]string{}
	for agent := range agents {
		entries, err := staging.List(agent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			projectOf[notify.SessionKey(e.Agent, e.SessionID)] = e.Project
		}
	}

	for k := range pending {
		i := strings.IndexByte(k, '/')
		if i <= 0 {
			counts[bucket{"(unknown)", "(unknown)"}]++
			continue
		}
		agent := k[:i]
		project := projectOf[k]
		if project == "" {
			project = "(unknown project)"
		}
		counts[bucket{agent, project}]++
	}

	keys := make([]bucket, 0, len(counts))
	for b := range counts {
		keys = append(keys, b)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].agent != keys[j].agent {
			return keys[i].agent < keys[j].agent
		}
		return keys[i].project < keys[j].project
	})

	for _, b := range keys {
		fmt.Fprintf(w, "    %s / %s: %d\n", b.agent, b.project, counts[b])
	}
}
