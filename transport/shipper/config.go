package shipper

import (
	"encoding/json"
	"fmt"
	"os"

	"loom/internal/config"
)

// Defaults applied by Config.Load when the on-disk file omits a field.
const (
	DefaultIntervalMinutes       = 10
	DefaultNotifyCooldownMinutes = 60
)

// Config is the shipper-only configuration carried in
// $LOOM_HOME/config.json. Path helpers live in internal/config; this
// struct lives with its sole consumer.
type Config struct {
	// ServerURL is the base URL of the loom-receiver (no trailing /v1/ingest).
	ServerURL string `json:"server_url"`

	// AuthToken is the shared bearer token sent as
	// "Authorization: Bearer <token>". May be empty for localhost/dev;
	// the receiver accepts empty if it too has no token.
	AuthToken string `json:"auth_token"`

	// IntervalMinutes is the in-process ticker cadence inside
	// `loom shipper daemon`. Note: launchd's StartInterval is no longer
	// used — the daemon owns its own cadence.
	IntervalMinutes int `json:"interval_minutes"`

	// NotifyOnFailure controls whether the shipper emits macOS
	// notifications on health-check, ship, or local errors. Pointer so
	// "unset" means default-on; explicit false disables.
	NotifyOnFailure *bool `json:"notify_on_failure,omitempty"`

	// NotifyCooldownMinutes is the minimum gap between two
	// notifications of the same kind. Unset/0 defaults to
	// DefaultNotifyCooldownMinutes; negative disables the cooldown
	// (notify every tick). To silence all notifications set
	// NotifyOnFailure: false.
	NotifyCooldownMinutes int `json:"notify_cooldown_minutes"`
}

// NotifyEnabled returns whether failure notifications should fire.
func (c *Config) NotifyEnabled() bool {
	if c.NotifyOnFailure == nil {
		return true
	}
	return *c.NotifyOnFailure
}

// LoadConfig reads $LOOM_HOME/config.json, applies defaults, and
// validates required fields.
func LoadConfig() (*Config, error) {
	path := config.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config not found at %s — create it with server_url and (optionally) auth_token", path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.IntervalMinutes <= 0 {
		c.IntervalMinutes = DefaultIntervalMinutes
	}
	if c.NotifyCooldownMinutes == 0 {
		c.NotifyCooldownMinutes = DefaultNotifyCooldownMinutes
	}
	if c.ServerURL == "" {
		return nil, fmt.Errorf("%s: server_url is required", path)
	}
	return &c, nil
}
