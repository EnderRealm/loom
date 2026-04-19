// loom-shipper walks agent session files, stages their new bytes locally, and
// ships staged deltas to the loom-receiver. Two-stage flow:
//
//  1. Capture: append new source bytes into ~/.loom/transport/staging so we
//     survive the agent cleaning up its own session files.
//  2. Ship: health-check the receiver, then post each staged session's delta
//     with retry+backoff and classified error logging.
//
// Commands:
//
//	loom-shipper once              single pass, ship and exit
//	loom-shipper install-agent     install a launchd user agent using config.interval_minutes
//	loom-shipper uninstall-agent   remove the launchd user agent
//	loom-shipper status            show launchctl state for the agent
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
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

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "once":
		runOnce()
	case "install-agent":
		installAgent()
	case "uninstall-agent":
		uninstallAgent()
	case "status":
		statusAgent()
	case "health":
		printHealth()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "loom-shipper - ship agent session deltas to the loom receiver\n\n")
	fmt.Fprintf(os.Stderr, "usage:\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper once              ship any new session bytes and exit\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper install-agent     install launchd user agent using interval_minutes from config\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper uninstall-agent   remove the launchd user agent\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper status            show launchctl state for the agent\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper health            show last-sync/pending-session state (from notify.state)\n\n")
	fmt.Fprintf(os.Stderr, "config: %s\n", config.Path())
	fmt.Fprintf(os.Stderr, "state:  %s\n", config.TransportDir())
}

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

// httpError carries an HTTP status plus body snippet so classify() can bucket it.
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
	// net errors: timeouts, connection refused, DNS, EOF mid-response.
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
	captured      int // sessions with captured bytes this tick
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

func runOnce() {
	if err := acquireLock(); err != nil {
		fmt.Fprintln(os.Stderr, "another shipper is running, skipping")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		die(err)
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

	// Capture always runs — local only, independent of receiver state.
	capturePass(&counts)

	// Health-check before touching the receiver. On failure, skip ship pass
	// but still refresh pending set from staging so the notification is accurate.
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
		shipPass(cfg, &counts)
		refreshPending(state)
		shipSummary = fmt.Sprintf("shipped=%d skipped=%d failed=%d%s",
			counts.shipped, counts.skipped, counts.failed, counts.classBreakdown())

		// Any session failures? One notification covering them all.
		if counts.failed > 0 {
			maybeNotify(cfg, state, notify.Event{
				Kind:    notify.KindShipFailed,
				Title:   "loom-shipper: ship failures",
				Summary: fmt.Sprintf("%d session%s failed to ship (%s)", counts.failed, pluralS(counts.failed), classSummary(counts.byClass)),
				Now:     now,
			})
		} else if counts.captureFailed == 0 {
			// Healthy tick: record success and fire recovery if appropriate.
			state.LastSuccessTS = now
			maybeNotify(cfg, state, notify.Event{
				Kind:    notify.KindRecovered,
				Title:   "loom-shipper: recovered",
				Summary: "Shipping is healthy again",
				Now:     now,
			})
		}
	}

	// Local/capture errors are independent of the receiver — notify once if any.
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

// ---------- capture pass ----------

// capturePass walks every registered adapter and appends new source bytes into
// staging. Each agent's source cursor advances only after a successful append
// so a crash mid-tick replays, not loses.
func capturePass(counts *tickCounts) {
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

// ---------- ship pass ----------

// shipPass drains every staged session (across all agents on disk, not just the
// registered ones — so a disabled adapter's leftovers still flush). Retries
// each session up to 3 times with jittered backoff on retryable errors.
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
	accepted, err := postIngestWithRetry(cfg, req, e)
	if err != nil {
		class := classify(err)
		// 409 resync: correct cursor and move on, not counted as failure.
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

// postIngestWithRetry makes up to 3 attempts with jittered backoff on net/5xx
// errors. 4xx / 409 / io errors return immediately — retrying won't help.
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

// jitter returns d scaled by a random factor in [0.7, 1.3].
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

// ---------- health check ----------

// healthCheck issues a 2s GET /healthz. Any transport error, timeout, or
// non-2xx response is treated as "receiver down".
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

// refreshPending rebuilds state.PendingSessions from on-disk cursors + staging
// sizes. A session is pending iff ship_cursor < staging file size.
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
		// Also include any staging entries that don't yet have a ship cursor.
		entries, err := staging.List(agent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			seen[e.SessionID] = true
		}
		for sid := range seen {
			// Find project for this session via staging.List result.
			var ent *staging.Entry
			for i := range entries {
				if entries[i].SessionID == sid {
					ent = &entries[i]
					break
				}
			}
			if ent == nil {
				// Cursor exists but staging gone — treat as not-pending.
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

// ---------- launchd agent management ----------

const agentLabel = "com.loom.shipper"

func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

// launchctlDomain returns the gui/<uid> domain target for the current user.
// gui/ is the standard for user-facing LaunchAgents; user/ would cover SSH-only
// sessions but loom is meant for the user's own interactive Mac.
func launchctlDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchctlTarget() string {
	return launchctlDomain() + "/" + agentLabel
}

func installAgent() {
	cfg, err := config.Load()
	if err != nil {
		die(err)
	}
	self, err := os.Executable()
	if err != nil {
		die(err)
	}
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		self = rp
	}

	plistPath, err := agentPlistPath()
	if err != nil {
		die(err)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		die(err)
	}
	if err := os.MkdirAll(config.TransportDir(), 0o700); err != nil {
		die(err)
	}

	interval := cfg.IntervalMinutes
	if interval <= 0 {
		interval = config.DefaultIntervalMinutes
	}
	intervalSeconds := interval * 60
	logPath := filepath.Join(config.TransportDir(), "shipper.log")

	// Bake LOOM_HOME into the plist only if the user set it explicitly. Otherwise
	// let the agent fall back to ~/.loom naturally at runtime.
	loomHome := os.Getenv("LOOM_HOME")

	plist := buildPlist(self, intervalSeconds, logPath, loomHome)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		die(err)
	}

	// Safety net: validate the plist before touching launchd. If this fails the
	// generator has a bug — delete the junk file so we don't poison ~/Library.
	if out, err := exec.Command("plutil", "-lint", plistPath).CombinedOutput(); err != nil {
		_ = os.Remove(plistPath)
		die(fmt.Errorf("plutil -lint: %v: %s", err, strings.TrimSpace(string(out))))
	}

	// Idempotency: bootout any previously-loaded instance before bootstrapping
	// the new one. bootout errors if nothing was loaded — intentionally ignored.
	_ = exec.Command("launchctl", "bootout", launchctlTarget()).Run()

	if out, err := exec.Command("launchctl", "bootstrap", launchctlDomain(), plistPath).CombinedOutput(); err != nil {
		die(fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out))))
	}

	fmt.Printf("installed launchd agent:\n")
	fmt.Printf("  label:    %s\n", agentLabel)
	fmt.Printf("  plist:    %s\n", plistPath)
	fmt.Printf("  interval: %ds (%d min)\n", intervalSeconds, interval)
	fmt.Printf("  log:      %s\n", logPath)
}

func uninstallAgent() {
	plistPath, err := agentPlistPath()
	if err != nil {
		die(err)
	}

	// bootout first. Tolerate "not loaded" errors — launchctl's exit codes are
	// poorly documented, so filter on message fragments.
	out, err := exec.Command("launchctl", "bootout", launchctlTarget()).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" &&
			!strings.Contains(msg, "No such process") &&
			!strings.Contains(msg, "Could not find service") &&
			!strings.Contains(msg, "not find specified service") {
			fmt.Fprintf(os.Stderr, "launchctl bootout: %s\n", msg)
		}
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		die(err)
	}

	fmt.Println("removed loom-shipper launchd agent")
}

func statusAgent() {
	out, err := exec.Command("launchctl", "print", launchctlTarget()).CombinedOutput()
	if err != nil {
		fmt.Println("loom-shipper agent is not loaded")
		return
	}
	fmt.Print(string(out))
}

// printHealth surfaces the same info carried in failure notifications: when
// the shipper last succeeded, how many sessions are unsynced, and which kind
// of failure (if any) is currently outstanding. install.sh --status calls this.
func printHealth() {
	state, err := notify.LoadState()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading notify state:", err)
		os.Exit(1)
	}
	now := time.Now().UTC()

	if state.LastSuccessTS.IsZero() {
		fmt.Println("  last successful sync: never")
	} else {
		fmt.Printf("  last successful sync: %s (%s ago)\n",
			state.LastSuccessTS.Local().Format("2006-01-02 15:04:05 MST"),
			notify.FormatDuration(now.Sub(state.LastSuccessTS)))
	}

	pending := len(state.PendingSessions)
	if pending == 0 {
		fmt.Println("  pending sessions:     0")
	} else {
		fmt.Printf("  pending sessions:     %d\n", pending)
		printPendingByProject(state.PendingSessions)
	}

	if state.LastNotifiedTS.IsZero() {
		fmt.Println("  last notification:    none")
	} else {
		fmt.Printf("  last notification:    %s at %s (%s ago)\n",
			state.LastNotifiedKind,
			state.LastNotifiedTS.Local().Format("2006-01-02 15:04:05 MST"),
			notify.FormatDuration(now.Sub(state.LastNotifiedTS)))
	}

	if state.FailureActive {
		fmt.Println("  failure active:       yes (next healthy tick will notify recovered)")
	} else {
		fmt.Println("  failure active:       no")
	}
}

// printPendingByProject prints a "<agent> / <project>: <n>" row per distinct
// (agent, project) pair, sorted and right-aligned on the count. PendingSessions
// only carries agent+session_id; we recover project by scanning staging. A
// session whose staging file is gone (e.g. manually cleaned) shows up under
// "(unknown project)" so the total still adds up.
func printPendingByProject(pending map[string]bool) {
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

	// agent+session_id → project, built once per agent so we do at most one
	// staging.List() call per agent regardless of pending count.
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
		fmt.Printf("    %s / %s: %d\n", b.agent, b.project, counts[b])
	}
}

// buildPlist generates the LaunchAgent plist XML. Paths are XML-escaped so
// weird characters (though unlikely in a binary path) don't corrupt the file.
func buildPlist(binaryPath string, intervalSeconds int, logPath, loomHome string) string {
	binE := xmlEscape(binaryPath)
	logE := xmlEscape(logPath)

	envBlock := ""
	if loomHome != "" {
		envBlock = fmt.Sprintf(`
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOOM_HOME</key>
        <string>%s</string>
    </dict>`, xmlEscape(loomHome))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>once</string>
    </array>
    <key>StartInterval</key>
    <integer>%d</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>%s
</dict>
</plist>
`, agentLabel, binE, intervalSeconds, logE, logE, envBlock)
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
