// Package config loads the shipper's per-user configuration from ~/.loom/config.json
// (override with LOOM_HOME). State (cursors, locks, logs) lives under the same root.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultIntervalMinutes        = 10
	DefaultNotifyCooldownMinutes  = 60
)

type Config struct {
	// ServerURL is the base URL of the loom-receiver (no trailing /v1/ingest).
	ServerURL string `json:"server_url"`

	// AuthToken is the shared bearer token sent as "Authorization: Bearer <token>".
	// May be empty for localhost/dev; the receiver will accept empty if it too has no token.
	AuthToken string `json:"auth_token"`

	// IntervalMinutes is how often install-cron will schedule the shipper to run.
	// The shipper itself is stateless with respect to this value — cron owns the tick.
	IntervalMinutes int `json:"interval_minutes"`

	// NotifyOnFailure controls whether the shipper emits macOS notifications on
	// health-check, ship, or local errors. Pointer so "unset" means default-on;
	// explicit false disables.
	NotifyOnFailure *bool `json:"notify_on_failure,omitempty"`

	// NotifyCooldownMinutes is the minimum gap between two notifications of the
	// same kind. Unset/0 defaults to DefaultNotifyCooldownMinutes; negative
	// disables the cooldown (notify every tick). To silence all notifications
	// set NotifyOnFailure: false.
	NotifyCooldownMinutes int `json:"notify_cooldown_minutes"`
}

// NotifyEnabled returns whether failure notifications should fire.
func (c *Config) NotifyEnabled() bool {
	if c.NotifyOnFailure == nil {
		return true
	}
	return *c.NotifyOnFailure
}

// Home returns the loom state root: $LOOM_HOME if set, else ~/.loom.
func Home() string {
	if h := os.Getenv("LOOM_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to cwd rather than panic; surfaces as a clear error on first write.
		return ".loom"
	}
	return filepath.Join(home, ".loom")
}

// Path returns the absolute path to config.json.
func Path() string {
	return filepath.Join(Home(), "config.json")
}

// TransportDir returns the directory that holds shipper state (cursors, lock, log).
func TransportDir() string {
	return filepath.Join(Home(), "transport")
}

// Load reads config.json, applies defaults, and validates required fields.
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config not found at %s — create it with server_url and (optionally) auth_token", Path())
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	if c.IntervalMinutes <= 0 {
		c.IntervalMinutes = DefaultIntervalMinutes
	}
	if c.NotifyCooldownMinutes == 0 {
		c.NotifyCooldownMinutes = DefaultNotifyCooldownMinutes
	}
	if c.ServerURL == "" {
		return nil, fmt.Errorf("%s: server_url is required", Path())
	}
	return &c, nil
}
