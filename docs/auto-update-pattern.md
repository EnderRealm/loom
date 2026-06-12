# Auto-update pattern: self-managing daemon over launchd

A small reusable template for Go-binary-on-launchd applications that
want to deploy themselves from `git push` without an external CI/CD
pipeline. Loom uses it (`internal/updater`); Ghostwheel originated it
(`tools/deployer`); both fit on a single MacBook or studio without
extra infrastructure.

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

**Source-bound.** The updater is a no-op for Homebrew installs (no
git checkout, no go toolchain). Don't `loom install updater` on those
hosts; rely on `brew upgrade` instead.

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

- [`Formula/loom.rb`](../Formula/loom.rb) — Homebrew formula path,
  for users who want the binary without the auto-update loop.
- [`internal/updater/updater.go`](../internal/updater/updater.go) —
  loom's concrete implementation of this pattern.
- Ghostwheel's [`tools/deployer/deployer.py`](https://github.com/EnderRealm/ghostwheel/blob/main/tools/deployer/deployer.py)
  — the Python original this pattern was extracted from.
