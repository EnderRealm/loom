# Auto-update pattern: self-managing daemon over launchd

A small reusable template for Go-binary-on-launchd applications that
want to deploy themselves without an external CI/CD pipeline.
Ghostwheel originated the source-build form (`tools/deployer`); it fits
on a single MacBook or studio without extra infrastructure. Loom now
runs the **artifact-fetch variant** (`internal/updater`) — it pulls
released tarballs instead of building from a checkout. The generic
source-build pattern is documented first; loom's variant follows in
"Loom's variant: release artifacts" below.

## When to use this pattern

✅ Single-host application (or small fleet) where every node has a
git checkout of the source.

✅ Build is cheap and reproducible (Go in particular: no link-step
download, no toolchain version drift).

✅ Daemons run under launchd KeepAlive — `kickstart -k` cleanly
respawns under a new binary.

✅ You're OK with "deploy = push to main." Branch protection +
required CI checks are your guardrails.

❌ Multi-host fleet without per-host source checkouts. Use a real
artifact registry + pull system (or a CI-driven SSH push) instead.

❌ Apps with stateful in-memory data that can't be lost on restart.
Auto-deploy = restart, period.

❌ Public distribution. Use Homebrew, Scoop, GitHub Releases. The
auto-updater here is for personal-fleet deployments.

## The shape

```
┌──────────────────────────────────────────────────────────────┐
│ launchd KeepAlive plist: com.<app>.updater                   │
│   ProgramArguments: <bin> updater daemon                     │
│   Env: APP_SOURCE, APP_UPDATER_INTERVAL_MINUTES              │
└──────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────┐
│ updater.Daemon — runs forever                                │
│   for {                                                      │
│     curSHA := git rev-parse HEAD                             │
│     git fetch origin main                                    │
│     remoteSHA := git rev-parse origin/main                   │
│     if curSHA == remoteSHA: sleep; continue                  │
│     git reset --hard origin/main                             │
│     go build -o $BIN ./cmd/<app>                             │
│     for label in [other agents]: launchctl kickstart -k      │
│     launchctl kickstart -k <updater-self>  ◄── kills us;     │
│                                                KeepAlive     │
│                                                respawns under│
│                                                new binary    │
│   }                                                          │
└──────────────────────────────────────────────────────────────┘
```

## Why this works

**Binary path pinning.** launchd plists pin the absolute binary path.
Rebuilding to the same path means the next respawn picks up the new
code without touching the plist. The updater never has to write
plists during a deploy — only on the initial install.

**Kickstart-self-last.** Restarting the other agents first means
their fresh code is already running by the time the updater dies.
After the updater respawns, every loom process on the machine is on
the new binary. There's no "half-deployed" window.

**KeepAlive bound to ThrottleInterval.** A bug in the new code that
makes the updater crash immediately would loop infinitely without a
throttle. `ThrottleInterval=30` (or higher) gives you a chance to
`launchctl bootout` it manually if you push something broken.

**Source-bound.** The updater needs a git checkout and a Go toolchain
on the host. A machine that has neither can't run `loom install
updater`; give it a source checkout and a Go toolchain first, then the
updater can manage it.

**Fast-forward-only guard.** `reset --hard` is unrecoverable, and on a
machine where the deploy checkout doubles as a dev checkout a naive
"HEAD != origin/main → reset" would destroy local work. The updater
deploys only on a pure fast-forward of a clean main. After the fetch
and before the reset it refuses the tick — logging the specific reason
and returning success (no error, so launchd doesn't crash-respawn) —
when any of:

- **the working tree is dirty** (`git status --porcelain` non-empty) —
  uncommitted edits would be clobbered;
- **HEAD is not on `main`** (`git rev-parse --abbrev-ref HEAD` != `main`,
  which also catches a detached HEAD mid-rebase) — a feature branch ref
  would be reset to `origin/main`;
- **HEAD is not an ancestor of `origin/main`**
  (`git merge-base --is-ancestor HEAD origin/main` exits non-zero) —
  local commits ahead of or diverged from the remote would be lost.

A skipped tick is not a failure; it converges on the next clean tick.
Once the conditions hold again — the dev commits land on `origin/main`,
the branch returns to `main`, the tree is committed or stashed — the
following poll fast-forwards and deploys as normal.

## Skeleton (Go)

A complete implementation is ~150 LoC. The key pieces (the skeleton's
`Tick` omits the fast-forward-only guard described above — see
`internal/updater/updater.go` for the guarded version before copying
this into a checkout that doubles as a dev tree):

```go
package updater

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

    "yourapp/internal/launchd"
)

const AgentLabel = "com.yourapp.updater"

var OtherAgents = []string{
    "com.yourapp.foo",
    "com.yourapp.bar",
}

func Daemon(ctx context.Context) error {
    src := os.Getenv("APP_SOURCE")
    if src == "" {
        home, _ := os.UserHomeDir()
        src = filepath.Join(home, "code", "yourapp")
    }
    interval := 5 * time.Minute
    if v := os.Getenv("APP_UPDATER_INTERVAL_MINUTES"); v != "" {
        var n int
        fmt.Sscanf(v, "%d", &n)
        if n > 0 {
            interval = time.Duration(n) * time.Minute
        }
    }

    Tick(ctx, src) // run once on startup
    t := time.NewTicker(interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-t.C:
            if err := Tick(ctx, src); err != nil {
                log.Printf("tick: %v", err)
            }
        }
    }
}

func Tick(ctx context.Context, src string) error {
    cur, _ := git(ctx, src, "rev-parse", "HEAD")
    if _, err := git(ctx, src, "fetch", "--quiet", "origin", "main"); err != nil {
        return err
    }
    remote, _ := git(ctx, src, "rev-parse", "origin/main")
    if cur == remote {
        return nil
    }
    if _, err := git(ctx, src, "reset", "--hard", "origin/main"); err != nil {
        return err
    }
    bin, _ := os.Executable()
    if err := exec.CommandContext(ctx, "go", "build",
        "-o", bin, "./cmd/yourapp").Run(); err != nil {
        return err
    }
    for _, label := range OtherAgents {
        launchd.Kickstart(label) // best-effort
    }
    return launchd.Kickstart(AgentLabel) // last
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
    c := exec.CommandContext(ctx, "git", args...)
    c.Dir = dir
    out, err := c.CombinedOutput()
    return strings.TrimSpace(string(out)), err
}
```

## Loom's variant: release artifacts

Loom no longer builds from a checkout. The same launchd-self-kickstart
spine drives an **artifact-fetch** updater that installs published
GitHub Release tarballs:

```
curVer := version.Current            // ldflag-stamped semver, "dev" on local builds
tag, assets := GET /repos/<owner>/<repo>/releases/latest
if curVer != "dev" && curVer == tag: up to date; sleep; continue
download loom_<ver>_<os>_<arch>.tar.gz + checksums.txt   (plain HTTPS, public repo)
verify sha256(tarball) against its checksums.txt line
extract the `loom` entry to a temp file beside the install target
chmod 0755; os.Rename over the target            ◄── atomic; never a partial write
for component in [other agents]: <bin> install <component>   ◄── bootout+bootstrap, waited
                                 verify pid start >= binary mtime  ◄── exit code is not proof
<bin> updater reexec (detached, Setsid)          ◄── re-bootstraps + verifies the updater job last
```

Why this variant over source-build:

- **No host toolchain.** Any machine can run the updater — no git
  checkout, no Go. The "source-bound" requirement below does not apply.
- **No `reset --hard` hazard.** There's no working tree to clobber, so
  the fast-forward-only guard is gone too. A machine where someone runs
  a local `go build` for development is unaffected; the updater only
  touches the pinned install path.
- **Released semver, not HEAD.** The deploy unit is a tagged release,
  not every push to `main`. Cutting a release (pushing a `vX.Y.Z` tag)
  is the deploy trigger; the installed binary's version is always a
  published semver.
- **Atomic + verified.** Download, checksum-verify, and extract all land
  in a temp file in the target's directory; only a verified extract is
  `os.Rename`d into place, so a failed or corrupt fetch leaves the
  running binary untouched.

The network is injected behind a small interface (`Latest` + `Download`)
so the install path is testable without hitting GitHub.

### macOS caveat: in-place swap needs bootout+bootstrap, not kickstart

The generic pattern above respawns agents with `launchctl kickstart -k`.
That does **not** work for the artifact-fetch variant on macOS. A
downloaded GoReleaser binary carries a different code signature than the
binary launchd originally bootstrapped, and launchd enforces a managed
Launch Constraint (LWCR) tied to that original signature:

- `launchctl kickstart` (no `-k`) is a no-op on an already-running
  service — the daemon keeps its stale in-memory code and never picks up
  the new binary.
- `launchctl kickstart -k` does restart the job, but launchd rejects the
  respawn of a differently-signed binary with `EX_CONFIG` ("needs LWCR
  update"); the daemon crash-loops (observed: shipper exit 78) instead of
  running the new code.

The reliable primitive is **bootout+bootstrap** — exactly what
`launchd.Install` (and therefore `loom install <component>`) does: it
restarts the daemon *and* re-registers the LWCR for the new binary. So
after installing the new binary the updater re-bootstraps each loaded
agent by shelling out to the freshly-installed binary's install
subcommand, `<bin> install <component>` (the updater package can't import
`cmd` without a cycle). These are role-neutral installs, so re-bootstrap
never rewrites the machine's persisted role.

- **Workers** (shipper, receiver, summarizer) run in their own launchd
  jobs, so the updater runs each `<bin> install <component>` and **waits**:
  the child boots out and bootstraps a *different* job and exits, leaving
  the updater process untouched. A failure on one logs and continues.
- **Self** (the updater) is re-bootstrapped **last**, and cannot be waited
  on: `<bin> install updater` boots out `com.loom.updater`, which is the
  updater's own job — that kills the updater process *and* the same-job
  child mid-bootout. Instead the updater spawns a **detached** helper
  (`<bin> updater reexec`, `SysProcAttr{Setsid: true}`) and returns
  without waiting. The helper sleeps ~2s so the updater process exits
  first, then bootout+bootstraps the updater job fresh under the new
  binary. A job cannot bootout itself from within; the detached helper is
  what escapes the parent job's teardown.

Only agents whose plist is present are re-bootstrapped (the same
"loaded" gating the kickstart path used).

**A successful install is not proof of a restart.** The bootout+bootstrap
path above (`loom/updater-re-bootstrap-92cf`) does run and always covered
every agent — the worker list has held all of them since it landed, and
`updater.log` shows all three loaded agents re-bootstrapped on the v1.2.2
update. Its gap is narrower: it reads `<bin> install <component>`'s exit
code as evidence that a new process came up. That exit code says the job
was re-registered. A job can be bootstrapped, enabled, and simply not
running, so agents observed serving the pre-update image for days had all
been "successfully" re-bootstrapped. Each install is now followed by a
check against the artifact itself:

- **Start time vs binary mtime.** The job's live pid (`launchctl print`,
  then `ps -o etime=` for its start time) must not predate the mtime of
  the binary just installed — an earlier start *is* the old image. A few
  seconds of slack absorbs the second-granular `etime` and the skew
  between the two clocks; a genuinely stale daemon is minutes to days out.
- **A bounded poll.** bootstrap is asynchronous, so the new process is not
  there the instant install returns. The check polls for a few seconds and
  reports the last failure it saw rather than failing on the first look.
- **One kickstart.** A job still processless partway through that window
  gets exactly one `launchctl kickstart`. bootstrap has re-registered the
  LWCR by then, so here kickstart nudges a job that bootstrapped without
  spawning, rather than being the (rejected) activation path above.

Every outcome is logged by label, including the one where the binary
can't be stat'd: with no mtime there is nothing to compare, so the log
says the image was not verified rather than claiming a restart it never
observed. The updater's own job is held to the same check by the detached
helper — the only process that outlives the teardown far enough to see the
result — whose stdout and stderr are appended to `updater.log`.

**Out-of-band replacement is not the updater's to catch.** When something
other than the updater writes the pinned binary — a local `go build`
installed over `~/.local/bin/loom` — no install runs, nothing
re-bootstraps, and the agents keep serving the replaced image while the
updater correctly reports `up to date`. No check inside the updater covers
a path it is never on. `loom status` runs the same start-time-vs-mtime
comparison per agent and flags the stale ones; that is the coverage for
this case.

**Recovery.** The self-re-bootstrap is the one step that can leave the
updater down: if the detached helper is killed after its `bootout` but
before its `bootstrap`, `com.loom.updater` is unloaded and stays down
(KeepAlive only respawns a *loaded* job, so it does not cover an
unloaded label). The worker agents are unaffected — they re-bootstrap
independently and stay current. If the updater stops polling after an
update (no new lines in `updater.log`), recover with a one-time
`loom install updater`. From then on it tracks releases again.

## Plist requirements

Three things the install path must bake into the plist:

1. **`KeepAlive=true`** — the updater suicides at the end of every
   deploy; KeepAlive is what brings it back under the new binary.

2. **`ThrottleIntervalSeconds=30`** (or higher) — bounds the
   crash-loop blast radius if you ship a broken updater.

3. **`PATH` env var** — launchd's default PATH is
   `/usr/bin:/bin:/usr/sbin:/sbin`, which doesn't include
   `/opt/homebrew/bin` (so no `git` on Apple Silicon) or the user's
   `~/go/bin`. Set a comprehensive PATH explicitly:

   ```go
   "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:" + home + "/go/bin",
   ```

## Operational notes

- **Branch protection on `main`.** The updater treats `origin/main`
  as authoritative. Bad code on main = bad code everywhere within
  one poll interval. Use required PR reviews + status checks.

- **No rollback.** This pattern only goes forward. Recovery is "push
  a revert commit." If you need rollback, add a "pin to specific
  SHA" mode and a way to set it remotely — but that's a bigger
  system than this template.

- **Log everything.** `~/.<app>/updater.log` should record every
  poll, every detected SHA bump, every rebuild, every kickstart. The
  log is your audit trail.

- **Hands-off period after first install.** New deployment? Watch
  the log for at least one full update cycle before walking away.
  The "kickstart self last" sequence is the highest-risk moment.

## Why not `go install` from a tag?

`go install yourapp@latest` works for end users but has two
properties this pattern avoids:

1. It requires Go on every host. The on-host build via the updater
   means you've already paid that cost.

2. It pulls from a module proxy with cache lag. The updater pulls
   directly from origin/main and is up-to-date within seconds of
   `git push`.

If those tradeoffs are fine, `go install` is simpler. Use the
updater pattern when you want sub-minute deploys and don't mind
keeping a source checkout per host.

## Related

- [`internal/updater/updater.go`](../internal/updater/updater.go) —
  loom's artifact-fetch variant of this pattern.
- Ghostwheel's [`tools/deployer/deployer.py`](https://github.com/EnderRealm/ghostwheel/blob/main/tools/deployer/deployer.py)
  — the Python original this pattern was extracted from.
