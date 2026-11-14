package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/shipper"
)

// systemLabels are the only daemons `loom install --system` installs, and so
// the only labels the sudoers grant names.
var systemLabels = []string{shipper.AgentLabel, updater.AgentLabel}

// Seams so tests drive system mode without root, a real account, /etc, or
// visudo. sudoersPath is the live grant; sudo's #includedir skips names
// containing a dot, which is what keeps writeSudoers' temp file inert.
var (
	geteuid     = os.Geteuid
	lookupUser  = user.Lookup
	sudoersPath = "/etc/sudoers.d/loom"
	chownRoot   = func(path string) error { return os.Chown(path, 0, 0) }
	visudoCheck = func(path string) error {
		out, err := exec.Command("/usr/sbin/visudo", "-cf", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// sudoersUserName bounds what reaches the sudoers file: a name with spaces,
// commas or colons would change the rule's meaning even if visudo accepted it.
var sudoersUserName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// installSystem installs component as a boot-time LaunchDaemon running as
// userName, for a host that boots with nobody logged in, and (re)writes the
// sudoers grant that lets that user restart loom's daemons after an update.
func installSystem(component, userName string) error {
	if component != "shipper" && component != "updater" {
		return fmt.Errorf("--system installs shipper or updater only, not %q", component)
	}
	if geteuid() != 0 {
		return fmt.Errorf("--system writes %s and %s; run it as root: sudo loom install --system --user <name> %s",
			launchd.SystemPlistDir, sudoersPath, component)
	}
	u, err := daemonUser(userName)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("--user %s: uid %q: %w", u.Username, u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("--user %s: gid %q: %w", u.Username, u.Gid, err)
	}
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	if err := checkUpdatable(bin, uid, u.Username); err != nil {
		return err
	}

	// Config, state, log and PATH resolution all derive from HOME and
	// LOOM_HOME. Under sudo those are root's (or unset), so point both at the
	// daemon user before anything resolves a path; they are baked into the
	// plist below so the daemon resolves the same ones.
	loomHome := os.Getenv("LOOM_HOME")
	if loomHome == "" {
		loomHome = filepath.Join(u.HomeDir, ".loom")
	}
	if err := os.Setenv("HOME", u.HomeDir); err != nil {
		return err
	}
	if err := os.Setenv("LOOM_HOME", loomHome); err != nil {
		return err
	}

	var spec launchd.Spec
	switch component {
	case "shipper":
		if _, err := shipper.LoadConfig(); err != nil {
			return err
		}
		spec = shipperSpec(bin, shipperLogPath())
	case "updater":
		spec = updaterSpec(bin, updater.LogPath())
	}
	spec.Domain = launchd.System
	spec.UserName = u.Username
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	spec.Env["HOME"] = u.HomeDir
	spec.Env["LOOM_HOME"] = loomHome
	spec.Env[launchd.DomainEnv] = launchd.SystemDomain

	// HOME is the daemon user's by now, so this resolves their LaunchAgent.
	// With both installed, a login would start a second copy on the same state.
	agentPlist, err := launchd.User.PlistPath(spec.Label)
	if err != nil {
		return err
	}
	if _, err := os.Stat(agentPlist); err == nil {
		return fmt.Errorf("%s already has a LaunchAgent for %s: %s; remove it as %s first "+
			"(launchctl bootout gui/%s/%s, then rm %s, or `loom uninstall` to remove all of %s's loom LaunchAgents)",
			u.Username, spec.Label, agentPlist, u.Username, u.Uid, spec.Label, agentPlist, u.Username)
	}

	if err := ensureDaemonPaths(uid, gid, spec.LogPath, loomHome, filepath.Dir(spec.LogPath)); err != nil {
		return err
	}
	// Before bootstrap: a freshly started updater may install a release on
	// its first tick, and its restart needs the grant in place.
	if err := writeSudoers(u.Username); err != nil {
		return err
	}
	if err := installAgent(spec); err != nil {
		return err
	}
	plistPath, _ := spec.Domain.PlistPath(spec.Label)
	fmt.Printf("installed %s (system daemon):\n", component)
	fmt.Printf("  label:    %s\n", spec.Label)
	fmt.Printf("  user:     %s\n", spec.UserName)
	fmt.Printf("  binary:   %s\n", bin)
	fmt.Printf("  plist:    %s\n", plistPath)
	fmt.Printf("  sudoers:  %s\n", sudoersPath)
	fmt.Printf("  log:      %s\n", spec.LogPath)
	return nil
}

// daemonUser resolves the --user a system daemon runs as, refusing a missing
// name, one unsafe to write into sudoers, an unknown account, and root.
func daemonUser(name string) (*user.User, error) {
	if name == "" {
		return nil, errors.New("--system needs --user <name>: the unprivileged user the daemon runs as")
	}
	if !sudoersUserName.MatchString(name) {
		return nil, fmt.Errorf("--user %q: not a plain account name", name)
	}
	u, err := lookupUser(name)
	if err != nil {
		return nil, fmt.Errorf("--user %s: %w", name, err)
	}
	if u.Uid == "0" {
		return nil, fmt.Errorf("--user %s is root; the daemon must run as an unprivileged user", name)
	}
	return u, nil
}

// checkUpdatable refuses a binary the daemon user could not replace. The
// updater, running as that user, stages the new release beside bin and
// renames it over, so the user must own bin and own a writable directory
// holding it.
func checkUpdatable(bin string, uid int, name string) error {
	for _, p := range []string{bin, filepath.Dir(bin)} {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid || (fi.IsDir() && fi.Mode().Perm()&0o200 == 0) {
			return fmt.Errorf("%s is not owned and writable by %s, so its updater could not replace %s; "+
				"install loom where %s owns it (e.g. ~%s/.local/bin) and run that copy under sudo", p, name, bin, name, name)
		}
	}
	return nil
}

// ensureDaemonPaths creates whichever of dirs (in order) and logPath are
// missing, owned by the daemon user. Created by root they would be root's,
// leaving the daemon unable to write its state or the log launchd opens for
// it. Existing paths are left as they are.
func ensureDaemonPaths(uid, gid int, logPath string, dirs ...string) error {
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			continue
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		return nil
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chown(logPath, uid, gid)
}

// sudoersContent is the grant for name: NOPASSWD for exactly the bootout
// and bootstrap command lines that restart each system daemon, rendered from
// the same argv the updater runs. sudo matches arguments exactly when a rule
// lists them, so nothing else is granted.
func sudoersContent(name string) string {
	var b strings.Builder
	b.WriteString("# Written by `loom install --system`: lets " + name + " restart loom's\n")
	b.WriteString("# boot-time daemons after an update. Removed by `loom uninstall --system`.\n")
	for _, label := range systemLabels {
		bootout, bootstrap := launchd.SystemRestartArgv(label)
		for _, argv := range [][]string{bootout, bootstrap} {
			fmt.Fprintf(&b, "%s ALL=(root) NOPASSWD: %s\n", name, strings.Join(argv, " "))
		}
	}
	return b.String()
}

// writeSudoers installs sudoersContent(name) at sudoersPath, root:wheel 0440.
// It is staged beside the target and only renamed into place once visudo
// has accepted it, so an invalid grant never becomes live and an existing one
// is left untouched on failure.
func writeSudoers(name string) error {
	tmp, err := os.CreateTemp(filepath.Dir(sudoersPath), ".loom.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", sudoersPath, err)
	}
	if _, err := tmp.WriteString(sudoersContent(name)); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmpPath, 0o440); err != nil {
		return fail(err)
	}
	if err := chownRoot(tmpPath); err != nil {
		return fail(err)
	}
	if err := visudoCheck(tmpPath); err != nil {
		return fail(fmt.Errorf("visudo rejected the grant: %w", err))
	}
	if err := os.Rename(tmpPath, sudoersPath); err != nil {
		return fail(err)
	}
	return nil
}

// uninstallSystem boots out and removes loom's system daemons and the
// sudoers grant.
func uninstallSystem() error {
	if geteuid() != 0 {
		return fmt.Errorf("--system removes %s plists and %s; run it as root: sudo loom uninstall --system",
			launchd.SystemPlistDir, sudoersPath)
	}
	for _, label := range systemLabels {
		if _, err := os.Stat(launchd.SystemPlistPath(label)); err != nil {
			continue
		}
		if err := launchd.System.Uninstall(label); err != nil {
			fmt.Fprintf(os.Stderr, "warn: uninstall %s: %v\n", label, err)
			continue
		}
		fmt.Printf("removed %s (system daemon)\n", label)
	}
	if err := os.Remove(sudoersPath); err == nil {
		fmt.Printf("removed %s\n", sudoersPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	fmt.Println("state preserved")
	return nil
}
