package cmd

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/shipper"
)

// The grant is the whole privilege boundary of system mode: exactly the four
// restart command lines, for one user, nothing wildcarded.
func TestSudoersContentExact(t *testing.T) {
	want := "# Written by `loom install --system`: lets engine restart loom's\n" +
		"# boot-time daemons after an update. Removed by `loom uninstall --system`.\n" +
		"engine ALL=(root) NOPASSWD: /bin/launchctl bootout system/com.loom.shipper\n" +
		"engine ALL=(root) NOPASSWD: /bin/launchctl bootstrap system /Library/LaunchDaemons/com.loom.shipper.plist\n" +
		"engine ALL=(root) NOPASSWD: /bin/launchctl bootout system/com.loom.updater\n" +
		"engine ALL=(root) NOPASSWD: /bin/launchctl bootstrap system /Library/LaunchDaemons/com.loom.updater.plist\n"
	if got := sudoersContent("engine"); got != want {
		t.Fatalf("sudoers =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(sudoersContent("engine"), "*") {
		t.Fatal("sudoers grant contains a wildcard")
	}
}

// The updater runs `sudo -n` plus exactly these argv; each must be a line of
// the grant or the restart fails at sudo.
func TestSudoersMatchesUpdaterArgv(t *testing.T) {
	content := sudoersContent("engine")
	for _, label := range []string{shipper.AgentLabel, updater.AgentLabel} {
		bootout, bootstrap := launchd.SystemRestartArgv(label)
		for _, argv := range [][]string{bootout, bootstrap} {
			line := "engine ALL=(root) NOPASSWD: " + strings.Join(argv, " ") + "\n"
			if !strings.Contains(content, line) {
				t.Errorf("grant lacks %q", line)
			}
		}
	}
}

// The rendered grant has to parse: run it through the real visudo, which
// checks syntax without root when pointed at a file.
func TestSudoersContentParses(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/visudo"); err != nil {
		t.Skip("visudo not available")
	}
	path := filepath.Join(t.TempDir(), "loom")
	if err := os.WriteFile(path, []byte(sudoersContent("engine")), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := visudoCheck(path); err != nil {
		t.Fatalf("visudo: %v", err)
	}
}

// stubSudoers points the grant at a temp dir and records visudo's input;
// visudoErr is what visudo reports.
func stubSudoers(t *testing.T, visudoErr error) (path string, checked *[]string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "loom")
	checked = new([]string)
	origPath, origChown, origVisudo := sudoersPath, chownRoot, visudoCheck
	sudoersPath = path
	chownRoot = func(string) error { return nil }
	visudoCheck = func(p string) error {
		*checked = append(*checked, p)
		return visudoErr
	}
	t.Cleanup(func() { sudoersPath, chownRoot, visudoCheck = origPath, origChown, origVisudo })
	return path, checked
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteSudoersValidatesBeforeInstalling(t *testing.T) {
	path, checked := stubSudoers(t, nil)

	if err := writeSudoers("engine"); err != nil {
		t.Fatalf("writeSudoers: %v", err)
	}
	if len(*checked) != 1 || (*checked)[0] == path {
		t.Fatalf("visudo checked %v, want the staged temp file only", *checked)
	}
	if !strings.HasPrefix(filepath.Base((*checked)[0]), ".") {
		t.Fatalf("staged file %s has no dot; sudo's includedir would read it", (*checked)[0])
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sudoersContent("engine") {
		t.Fatalf("installed grant = %q", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o440 {
		t.Fatalf("mode = %v, want 0440", fi.Mode().Perm())
	}
	if names := dirEntries(t, filepath.Dir(path)); len(names) != 1 {
		t.Fatalf("dir = %v, want only the grant", names)
	}
}

// A grant visudo rejects never becomes live, and the previous grant stays.
func TestWriteSudoersRejectedLeavesExisting(t *testing.T) {
	path, _ := stubSudoers(t, errors.New("syntax error"))
	if err := os.WriteFile(path, []byte("previous\n"), 0o440); err != nil {
		t.Fatal(err)
	}

	err := writeSudoers("engine")
	if err == nil || !strings.Contains(err.Error(), "visudo") {
		t.Fatalf("writeSudoers err = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "previous\n" {
		t.Fatalf("grant = %q, want the previous one untouched", got)
	}
	if names := dirEntries(t, filepath.Dir(path)); len(names) != 1 {
		t.Fatalf("dir = %v, want no staged file left", names)
	}
}

// stubSystem makes this process look like root installing for an account
// that is really the test's own user (so chown succeeds unprivileged), with
// HOME at a temp dir. HOME and LOOM_HOME are restored after the test because
// installSystem retargets them.
func stubSystem(t *testing.T, euid int) (u *user.User, home string) {
	t.Helper()
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	u = &user.User{Uid: me.Uid, Gid: me.Gid, Username: "engine", HomeDir: home}
	t.Setenv("HOME", os.Getenv("HOME"))
	t.Setenv("LOOM_HOME", "")
	origEuid, origLookup := geteuid, lookupUser
	geteuid = func() int { return euid }
	lookupUser = func(name string) (*user.User, error) {
		if name != "engine" {
			return nil, user.UnknownUserError(name)
		}
		return u, nil
	}
	t.Cleanup(func() { geteuid, lookupUser = origEuid, origLookup })
	return u, home
}

func TestInstallSystemRefuses(t *testing.T) {
	cases := []struct {
		name, component, user string
		euid                  int
		root                  bool
		want                  string
	}{
		{"agent-only component", "receiver", "engine", 0, false, "shipper or updater only"},
		{"profile", "server", "engine", 0, false, "shipper or updater only"},
		{"not root", "updater", "engine", 501, false, "run it as root"},
		{"no user", "updater", "", 0, false, "needs --user"},
		{"unknown user", "updater", "nobody-here", 0, false, "nobody-here"},
		{"unsafe name", "updater", "engine ALL", 0, false, "not a plain account name"},
		{"root user", "updater", "engine", 0, true, "is root"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, _ := stubSystem(t, c.euid)
			if c.root {
				u.Uid = "0"
			}
			calls, _, _ := stubInstall(t, nil)
			path, checked := stubSudoers(t, nil)

			err := installSystem(c.component, c.user)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("installSystem err = %v, want %q", err, c.want)
			}
			if len(*calls) != 0 || len(*checked) != 0 {
				t.Fatalf("refusal still installed: calls=%v sudoers=%v", *calls, *checked)
			}
			if _, err := os.Stat(path); err == nil {
				t.Fatal("refusal still wrote the sudoers grant")
			}
		})
	}
}

// The updater has to replace the binary as the daemon user, so a binary that
// user does not own is refused before anything is written.
func TestInstallSystemRefusesBinaryTheUserCannotReplace(t *testing.T) {
	u, _ := stubSystem(t, 0)
	uid, _ := strconv.Atoi(u.Uid)
	u.Uid = strconv.Itoa(uid + 1)
	calls, _, _ := stubInstall(t, nil)
	_, checked := stubSudoers(t, nil)

	err := installSystem("updater", "engine")
	if err == nil || !strings.Contains(err.Error(), "not owned and writable by engine") {
		t.Fatalf("installSystem err = %v", err)
	}
	if len(*calls) != 0 || len(*checked) != 0 {
		t.Fatalf("refusal still installed: calls=%v sudoers=%v", *calls, *checked)
	}
}

// A boot-time daemon beside the daemon user's own LaunchAgent for the same
// label would run a second copy whenever that user logs in, so system mode
// refuses before writing anything.
func TestInstallSystemRefusesExistingAgent(t *testing.T) {
	for _, c := range []struct{ component, label string }{
		{"shipper", shipper.AgentLabel},
		{"updater", updater.AgentLabel},
	} {
		t.Run(c.component, func(t *testing.T) {
			_, home := stubSystem(t, 0)
			if c.component == "shipper" {
				writeLoomConfig(t, `{"server_url":"https://loom.example"}`)
			}
			calls, _, _ := stubInstall(t, nil)
			path, checked := stubSudoers(t, nil)
			agentDir := filepath.Join(home, "Library", "LaunchAgents")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			plist := filepath.Join(agentDir, c.label+".plist")
			if err := os.WriteFile(plist, []byte("plist\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			err := installSystem(c.component, "engine")
			if err == nil || !strings.Contains(err.Error(), plist) {
				t.Fatalf("installSystem err = %v, want it to name %s", err, plist)
			}
			if len(*calls) != 0 || len(*checked) != 0 {
				t.Fatalf("refusal still installed: calls=%v sudoers=%v", *calls, *checked)
			}
			if _, err := os.Stat(path); err == nil {
				t.Fatal("refusal still wrote the sudoers grant")
			}
			for _, dir := range []string{filepath.Join(home, ".loom"), filepath.Join(os.Getenv("LOOM_HOME"), "transport")} {
				if _, err := os.Stat(dir); err == nil {
					t.Fatalf("refusal still created %s", dir)
				}
			}
		})
	}
}

func TestInstallSystemUpdater(t *testing.T) {
	_, home := stubSystem(t, 0)
	calls, installed, _ := stubInstall(t, nil)
	path, _ := stubSudoers(t, nil)

	if err := installSystem("updater", "engine"); err != nil {
		t.Fatalf("installSystem: %v", err)
	}
	if got := strings.Join(*calls, ","); got != "install,await" {
		t.Fatalf("calls = %s", got)
	}
	loomHome := filepath.Join(home, ".loom")
	if installed.Domain != launchd.System || installed.UserName != "engine" || installed.Label != updater.AgentLabel {
		t.Fatalf("spec = %+v", *installed)
	}
	for k, want := range map[string]string{
		"HOME":            home,
		"LOOM_HOME":       loomHome,
		launchd.DomainEnv: launchd.SystemDomain,
	} {
		if got := installed.Env[k]; got != want {
			t.Errorf("env %s = %q, want %q", k, got, want)
		}
	}
	// Resolved for the daemon user, not the invoking one.
	if installed.LogPath != filepath.Join(loomHome, "updater.log") {
		t.Fatalf("log = %s", installed.LogPath)
	}
	if !strings.HasPrefix(installed.Env["PATH"], filepath.Join(home, ".local", "bin")) {
		t.Fatalf("PATH = %s, want the daemon user's ~/.local/bin first", installed.Env["PATH"])
	}
	if _, err := os.Stat(installed.LogPath); err != nil {
		t.Fatalf("log not created for the daemon: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != sudoersContent("engine") {
		t.Fatalf("grant = %q", got)
	}
}

func TestInstallSystemShipper(t *testing.T) {
	stubSystem(t, 0)
	// An explicit LOOM_HOME (sudo LOOM_HOME=... loom install ...) wins over
	// the daemon user's ~/.loom.
	writeLoomConfig(t, `{"server_url":"https://loom.example"}`)
	loomHome := os.Getenv("LOOM_HOME")
	_, installed, _ := stubInstall(t, nil)
	stubSudoers(t, nil)

	if err := installSystem("shipper", "engine"); err != nil {
		t.Fatalf("installSystem: %v", err)
	}
	if installed.Label != shipper.AgentLabel || installed.Domain != launchd.System || installed.UserName != "engine" {
		t.Fatalf("spec = %+v", *installed)
	}
	if installed.Env["LOOM_HOME"] != loomHome || installed.Env[launchd.DomainEnv] != launchd.SystemDomain {
		t.Fatalf("env = %v", installed.Env)
	}
	if installed.LogPath != filepath.Join(loomHome, "transport", "shipper.log") {
		t.Fatalf("log = %s", installed.LogPath)
	}
	if _, err := os.Stat(installed.LogPath); err != nil {
		t.Fatalf("log not created for the daemon: %v", err)
	}
}

func TestUserFlagRequiresSystem(t *testing.T) {
	origSystem, origUser := installSystemFlag, installUserFlag
	installSystemFlag, installUserFlag = false, "engine"
	t.Cleanup(func() { installSystemFlag, installUserFlag = origSystem, origUser })
	calls, _, _ := stubInstall(t, nil)

	err := installCmd.RunE(installCmd, []string{"updater"})
	if err == nil || !strings.Contains(err.Error(), "--system") {
		t.Fatalf("RunE err = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %v, want none", *calls)
	}
}

func TestUninstallSystemRequiresRoot(t *testing.T) {
	stubSystem(t, 501)
	path, _ := stubSudoers(t, nil)
	if err := os.WriteFile(path, []byte("grant\n"), 0o440); err != nil {
		t.Fatal(err)
	}

	if err := uninstallSystem(); err == nil || !strings.Contains(err.Error(), "run it as root") {
		t.Fatalf("uninstallSystem err = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("non-root uninstall removed the grant")
	}
}
