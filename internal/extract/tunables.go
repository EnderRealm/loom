package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loom/internal/config"
)

// The extractor's tunables. A launchd job inherits nothing from a login
// shell, and the updater re-bootstraps this agent by shelling
// `loom install extractor` from its own daemon environment — which carries
// only LOOM_HOME, the updater interval and PATH. Capturing them from the
// installing process alone therefore loses them on the first auto-update, so
// they are persisted under LOOM_HOME at install time and resolved from there,
// with the process environment as a one-off override.
const (
	EnvExtractorsDir = "LOOM_EXTRACTORS_DIR"
	EnvKnowledgeRoot = "LOOM_KNOWLEDGE_ROOT"
	EnvProvider      = "LOOM_EXTRACT_PROVIDER"
	EnvModel         = "LOOM_EXTRACT_MODEL"
)

// EnvLoomBin names the binary extract.py writes the knowledge store through.
// Not a tunable: it is resolved per run from the running executable, so it is
// neither persisted nor baked into the plist, where it would pin a build that
// the updater has since replaced.
const EnvLoomBin = "LOOM_BIN"

// tunableKeys is the persisted set, and the set baked into the plist.
var tunableKeys = []string{EnvExtractorsDir, EnvKnowledgeRoot, EnvProvider, EnvModel}

// Settings is the extractor's effective configuration — what a sweep will
// actually use, as opposed to whatever the invoking shell exports. Reported
// by `loom status`.
type Settings struct {
	ExtractorsDir string
	KnowledgeRoot string
	Provider      string
	Model         string
}

// CurrentSettings resolves every tunable, defaults applied.
func CurrentSettings() Settings {
	return Settings{
		ExtractorsDir: ExtractorsDir(),
		KnowledgeRoot: knowledgeRoot(),
		Provider:      tunableOr(EnvProvider, defaultProvider),
		Model:         tunableOr(EnvModel, defaultModel),
	}
}

// PersistTunables writes the resolved tunables under LOOM_HOME and returns
// them for the caller to bake into the agent's plist. Values already
// persisted survive an install run whose environment doesn't set them, so a
// re-bootstrap from any environment reproduces the same plist.
func PersistTunables() (map[string]string, error) {
	resolved := map[string]string{}
	var b strings.Builder
	for _, k := range tunableKeys {
		v := tunable(k)
		if v == "" {
			continue
		}
		resolved[k] = v
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tunablesPath(), []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	return resolved, nil
}

func tunablesPath() string {
	return filepath.Join(config.Home(), "extract-env")
}

// tunable resolves one tunable: the process environment wins so a one-off
// `loom extract` can override the agent's configuration, else the value
// persisted at install time, else "".
func tunable(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return strings.TrimSpace(loadTunables()[key])
}

func tunableOr(key, fallback string) string {
	if v := tunable(key); v != "" {
		return v
	}
	return fallback
}

// loadTunables reads the persisted key=value file. A missing or unreadable
// file is an empty set — the agent falls back to its defaults rather than
// failing to sweep.
func loadTunables() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(tunablesPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}
