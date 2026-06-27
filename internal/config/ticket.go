package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CentralStoreRoot reads the tk central store root from ~/.ticket/config.yaml.
// loom carries no YAML dependency, so it scans for the single top-level
// `central_root:` key rather than parsing the whole document.
func CentralStoreRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("tk central store not configured: %w", err)
	}
	cfgPath := filepath.Join(home, ".ticket", "config.yaml")
	f, err := os.Open(cfgPath)
	if err != nil {
		return "", fmt.Errorf("tk central store not configured: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // nested key, not the top-level central_root
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "central_root:"); ok {
			root := strings.TrimSpace(v)
			if root == "" {
				break
			}
			return root, nil
		}
	}
	return "", fmt.Errorf("tk central store not configured: no central_root in %s", cfgPath)
}
