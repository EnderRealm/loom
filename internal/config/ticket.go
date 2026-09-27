package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tkStoreRootEnv is tk's own override of the central store root
// (project.StoreRootEnv in the ticket module). Honored here on tk's terms so
// loom's direct reads of the central store and the library's own project
// resolution — which reads the variable itself — point at the same store.
const tkStoreRootEnv = "TK_STORE_ROOT"

// CentralStoreRoot reads the tk central store root: TK_STORE_ROOT when it is
// set, else the `central_root:` key of ~/.ticket/config.yaml. loom carries no
// YAML dependency, so it scans for the single top-level key rather than
// parsing the whole document.
//
// A set-but-unusable override — empty, or not an absolute path — is an error
// and never a fall-through to the configured root: the override exists so a
// harness can point a run at a throwaway store, and silently resolving the
// real one from a broken override is the failure it exists to prevent. The
// rule is tk's (project.StoreRootOverride), kept in step by hand.
func CentralStoreRoot() (string, error) {
	if root, ok := os.LookupEnv(tkStoreRootEnv); ok {
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", tkStoreRootEnv, root)
		}
		return root, nil
	}
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

// ProjectRepoPath reads the repository registered for project on this
// machine: `projects.<project>.path` in tk's local config, which is
// TK_STORE_ROOT's own .ticket/config.yaml when the override is set and
// ~/.ticket/config.yaml otherwise (tk's project.ConfigPath). Scanned line by
// line for the same reason as CentralStoreRoot; a project name is the first
// indent level under the top-level `projects:` key, its path one level
// deeper.
func ProjectRepoPath(project string) (string, error) {
	var cfgPath string
	if root, ok := os.LookupEnv(tkStoreRootEnv); ok {
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", tkStoreRootEnv, root)
		}
		cfgPath = filepath.Join(root, ".ticket", "config.yaml")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("tk config not found: %w", err)
		}
		cfgPath = filepath.Join(home, ".ticket", "config.yaml")
	}
	f, err := os.Open(cfgPath)
	if err != nil {
		return "", fmt.Errorf("tk config not found: %w", err)
	}
	defer f.Close()

	inProjects, inProject := false, false
	nameIndent := -1
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inProjects, inProject = trimmed == "projects:", false
			continue
		}
		if !inProjects {
			continue
		}
		if nameIndent < 0 {
			nameIndent = indent
		}
		if indent <= nameIndent {
			inProject = indent == nameIndent && trimmed == project+":"
			continue
		}
		if v, ok := strings.CutPrefix(trimmed, "path:"); ok && inProject {
			if p := strings.TrimSpace(v); p != "" {
				return p, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", cfgPath, err)
	}
	return "", fmt.Errorf("project %q has no repository registered in %s", project, cfgPath)
}
