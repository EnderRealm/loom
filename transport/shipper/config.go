package shipper

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"loom/internal/config"
)

// Defaults applied by Config.Load when the on-disk file omits a field.
const (
	DefaultIntervalMinutes       = 10
	DefaultNotifyCooldownMinutes = 60
)

// DefaultSummarizerInterval is the summarizer's watch-mode sweep cadence
// when summarizer_interval_seconds is absent.
const DefaultSummarizerInterval = 30 * time.Second

// Config is $LOOM_HOME/config.json for both roles: the shipper's fields,
// validated by LoadConfig, and the seconds cadences, validated by
// readConfig, which the server role reads without a server_url. Path
// helpers live in internal/config.
type Config struct {
	// ServerURL is the base URL of the loom-receiver (no trailing /v1/ingest).
	ServerURL string `json:"server_url"`

	// AuthToken is the shared bearer token sent as
	// "Authorization: Bearer <token>". May be empty for localhost/dev;
	// the receiver accepts empty if it too has no token.
	AuthToken string `json:"auth_token"`

	// IntervalMinutes is the in-process ticker cadence inside
	// `loom shipper daemon` when IntervalSeconds is absent. Note: launchd's
	// StartInterval is no longer used — the daemon owns its own cadence.
	IntervalMinutes int `json:"interval_minutes"`

	// IntervalSeconds is the same cadence in seconds and takes precedence
	// over IntervalMinutes when set. Pointer so absent is told from an
	// explicit value: zero and negative are rejected, never defaulted.
	IntervalSeconds *int `json:"interval_seconds,omitempty"`

	// SummarizerIntervalSeconds is the summarizer's watch-mode sweep
	// cadence, baked into its plist by `loom install summarizer`. Read on
	// the server role, which has no server_url, so SummarizerInterval
	// reads the file without LoadConfig's shipper validation.
	SummarizerIntervalSeconds *int `json:"summarizer_interval_seconds,omitempty"`

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

// Interval is the shipper tick cadence: IntervalSeconds when set, else
// IntervalMinutes (or its default).
func (c *Config) Interval() time.Duration {
	if c.IntervalSeconds != nil {
		return time.Duration(*c.IntervalSeconds) * time.Second
	}
	minutes := c.IntervalMinutes
	if minutes <= 0 {
		minutes = DefaultIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// SummarizerInterval is the summarizer sweep cadence:
// SummarizerIntervalSeconds when set, else DefaultSummarizerInterval.
func (c *Config) SummarizerInterval() time.Duration {
	if c.SummarizerIntervalSeconds != nil {
		return time.Duration(*c.SummarizerIntervalSeconds) * time.Second
	}
	return DefaultSummarizerInterval
}

// LoadConfig reads $LOOM_HOME/config.json, applies defaults, and
// validates required fields.
func LoadConfig() (*Config, error) {
	path := config.Path()
	c, err := readConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config not found at %s — create it with server_url and (optionally) auth_token", path)
		}
		return nil, err
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
	return c, nil
}

// SummarizerInterval reads the summarizer cadence from config.json. A
// missing file is the server role's normal state and yields the default;
// only the seconds fields are validated, since a server has no server_url.
func SummarizerInterval() (time.Duration, error) {
	_, summarize, err := Cadences()
	return summarize, err
}

// Cadences reads both tick cadences off config.json for a reader judging
// how stale the shipper's sync and the summarizer's sweep are. A missing
// file yields the defaults, and server_url is not required.
func Cadences() (ship, summarize time.Duration, err error) {
	c, err := readConfig(config.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultIntervalMinutes * time.Minute, DefaultSummarizerInterval, nil
		}
		return 0, 0, err
	}
	return c.Interval(), c.SummarizerInterval(), nil
}

// readConfig parses the file and validates the seconds fields, which are
// shared by both roles. A missing file is returned as the raw os error so
// each caller can decide what absence means.
func readConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := validateSeconds(path, "interval_seconds", c.IntervalSeconds); err != nil {
		return nil, err
	}
	if err := validateSeconds(path, "summarizer_interval_seconds", c.SummarizerIntervalSeconds); err != nil {
		return nil, err
	}
	return &c, nil
}

func validateSeconds(path, field string, v *int) error {
	if v != nil && *v <= 0 {
		return fmt.Errorf("%s: %s must be a positive number of seconds, got %d", path, field, *v)
	}
	return nil
}
