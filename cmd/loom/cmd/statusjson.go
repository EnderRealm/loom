package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/shipper"
)

// The states `loom status --json` reports where launchd has none of its own.
const (
	stateNotInstalled = "not-installed"
	stateNotLoaded    = "not-loaded"
	stateUnknown      = "unknown"
)

// statusJSON is `loom status --json`. A null is a value that does not exist,
// or one that could not be read when the error beside it is set.
type statusJSON struct {
	Role       *string                  `json:"role"`
	RoleError  *string                  `json:"role_error"`
	Components map[string]componentJSON `json:"components"`
	Sync       *syncJSON                `json:"sync"`
}

type componentJSON struct {
	Installed      *bool      `json:"installed"`
	Expected       *bool      `json:"expected"`
	State          string     `json:"state"`
	LastExitCode   *int       `json:"last_exit_code"`
	LastExitSignal *string    `json:"last_exit_signal"`
	LastExitReason *string    `json:"last_exit_reason"`
	LastActivity   *time.Time `json:"last_activity"`
	Error          *string    `json:"error"`
}

type syncJSON struct {
	LastSuccess       *time.Time `json:"last_success"`
	UnshippedSessions *int       `json:"unshipped_sessions"`
	FailureActive     *bool      `json:"failure_active"`
	Error             *string    `json:"error"`
}

// printStatusJSON reports a reading it could not take in the object and
// returns an error only when no object can be printed, so one broken component
// does not cost the caller the others.
func printStatusJSON(w io.Writer) error {
	// config.Home falls back to the working directory here, where every file
	// is absent and the object would read as a clean first run.
	if os.Getenv("LOOM_HOME") == "" {
		if _, err := os.UserHomeDir(); err != nil {
			return fmt.Errorf("resolve loom state root: %w", err)
		}
	}
	role, roleErr := config.LoadRole()
	report := statusJSON{
		Role:       nonEmpty(role),
		RoleError:  errText(roleErr),
		Components: map[string]componentJSON{},
	}
	for _, r := range agentReports(role) {
		c := readComponent(r)
		if roleErr != nil {
			c.Expected = nil
		}
		report.Components[r.name] = c
		// Read unless the shipper is known to be absent: a plist that can't be
		// resolved must not hide a failing sync.
		if r.label == shipper.AgentLabel && c.State != stateNotInstalled {
			report.Sync = readSync()
		}
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(b))
	return nil
}

func readComponent(r agentReport) componentJSON {
	c := componentJSON{Expected: &r.expected, State: stateUnknown}
	var errs []error

	d, _, installed, err := agentPlist(r.label)
	switch {
	case err != nil:
		errs = append(errs, err)
		if r.label == updater.AgentLabel {
			// The updater is expected only where its plist is.
			c.Expected = nil
		}
	case !installed:
		c.Installed = &installed
		c.State = stateNotInstalled
		return c
	default:
		c.Installed = &installed
		out, loaded, err := d.Print(r.label)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !loaded:
			c.State = stateNotLoaded
		default:
			job, err := launchd.JobFromPrint(out)
			errs = append(errs, err)
			if job.State != "" {
				c.State = job.State
			}
			c.LastExitCode = job.LastExitCode
			c.LastExitSignal = nonEmpty(job.LastExitSignal)
			c.LastExitReason = nonEmpty(job.LastExitReason)
		}
	}

	fi, err := os.Stat(r.logPath)
	switch {
	case err == nil:
		mtime := fi.ModTime().UTC()
		c.LastActivity = &mtime
	case !errors.Is(err, os.ErrNotExist):
		errs = append(errs, err)
	}
	c.Error = errText(errors.Join(errs...))
	return c
}

func readSync() *syncJSON {
	h, err := shipper.ReadSyncHealth()
	return &syncJSON{
		LastSuccess:       h.LastSuccess,
		UnshippedSessions: h.UnshippedSessions,
		FailureActive:     h.FailureActive,
		Error:             errText(err),
	}
}

// nonEmpty and errText map "nothing to report" to JSON null.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func errText(err error) *string {
	if err == nil {
		return nil
	}
	return nonEmpty(err.Error())
}
