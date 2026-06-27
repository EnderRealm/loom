# Changelog

All notable changes to Loom are recorded here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioning follows [SemVer](https://semver.org/spec/v2.0.0.html).

## [1.2.2] — 2026-06-27 — Persisted receiver token

### Fixed

- Receiver bearer token is now persisted to `~/.loom/receiver-token`
  (0600) instead of living only in the receiver plist's
  `EnvironmentVariables`. `loom install receiver` resolves the token from
  `LOOM_RECEIVER_TOKEN` (seeding the file), then the persisted file, then
  an interactive prompt; non-interactive installs with neither error
  rather than blocking on stdin. The receiver daemon reads the persisted
  token at runtime, and the token no longer appears in the plist (so it's
  not exposed via the plist file or `launchctl print`). This fixes the
  updater's re-bootstrap of the receiver, which shells `loom install
  receiver` with no token in its env.

## [1.2.1] — 2026-06-20 — Reliable release activation

### Fixed

- Auto-updater now activates a new release binary on macOS. After
  installing the downloaded artifact it bootout+bootstraps each loaded
  loom agent (via `<bin> install <component>`) instead of `launchctl
  kickstart`. `kickstart` without `-k` is a no-op on a running daemon
  (stale in-memory code), and even `kickstart -k` can't respawn a
  differently-signed binary under launchd's managed Launch Constraint
  (`EX_CONFIG`), so the new code never ran. The updater re-bootstraps
  itself last via a detached `loom updater reexec` helper, because a job
  can't bootout its own launchd job from within. Re-bootstrap uses
  role-neutral component installs, so it never changes the machine's
  persisted role.

## [1.2.0] — 2026-06-20 — Release-driven updates

### Added

- `loom relevant --for-ticket <namespaced-id>`: read-only command that
  scans the validated knowledge corpus at `~/.loom/knowledge/` and prints
  a ranked markdown list of related truths and decisions. Ranking
  precedence is direct citations → evidence-path overlap → keyword
  overlap, with scope match breaking ties; each line carries the artifact
  id, claim title, relative path, score, and matched signal.
- Machine roles: `loom install server` (receiver + summarizer) and the
  new `loom install remote` (shipper) record the machine's role under
  `$LOOM_HOME/role`. `loom dev` and `loom status` scope health to the
  daemons that role expects, so a shipper-only machine no longer reports
  "degraded" for the receiver/summarizer it was never meant to run. The
  updater counts toward health only when its plist is installed.

### Changed

- Auto-updater now installs released GitHub Release artifacts instead of
  building `origin/main` from a source checkout. Each tick compares the
  running binary's version to the latest release, and on a newer release
  downloads the platform tarball (`loom_<ver>_<os>_<arch>.tar.gz`),
  verifies its `checksums.txt` entry, atomically installs the extracted
  binary over `~/.local/bin/loom`, and kickstarts every agent (itself
  last). This needs no git checkout and no Go toolchain on the host, and
  removes the `reset --hard` hazard entirely. `loom install updater` no
  longer requires a checkout (drops the `LOOM_SOURCE` env var). The
  released-artifact updater is the single install/update channel;
  development is a separate local `go build -o ./loom` (or `make dev`).
- `loom dev` health line shows the role and a per-role daemon count
  (e.g. `remote · 1/1 daemons`); with no role set it keeps the legacy
  format and prints a hint to run `loom install server`/`remote`.
- `loom status` prints the machine role, marks role-expected components
  that aren't installed instead of skipping them silently, and shows the
  sync-health section only when the shipper is installed.

### Removed

- Homebrew distribution channel. The `EnderRealm/homebrew-tools` tap
  publishing step is gone from `.goreleaser.yml` (no more
  `TAP_GITHUB_TOKEN`), and the brew install/upgrade instructions and
  tap-trust / cask-collision caveats are gone from the README. Loom
  deploys through a single channel: a source checkout plus the `loom
  updater` daemon on every fleet machine. Tagged GitHub releases stay
  as milestone markers and artifact stores, not an install path.

### Fixed

- Auto-updater no longer destroys local work on machines where the
  deploy checkout doubles as a dev checkout. The updater treated any
  `HEAD != origin/main` as "remote moved forward" and ran `reset
  --hard origin/main`, clobbering uncommitted changes, resetting
  unpushed commits, and overwriting checked-out feature branch refs. A
  tick now deploys only on a pure fast-forward of a clean `main`;
  otherwise it logs the reason (dirty tree / not on main / HEAD
  diverged) and skips, converging on the next clean tick.

## [1.1.1] — 2026-06-09 — Automated releases

### Added

- GoReleaser release pipeline (`.goreleaser.yml` +
  `.github/workflows/release.yml`): pushing a `vX.Y.Z` tag
  cross-compiles `loom` for darwin/linux × amd64/arm64, publishes a
  GitHub Release with binary tarballs + `checksums.txt`, and commits
  the generated formula to the `EnderRealm/homebrew-tools` tap. See
  `docs/releasing.md`.

### Changed

- Homebrew install moves to the shared `EnderRealm/homebrew-tools` tap
  (`brew install enderrealm/tools/loom`) and ships prebuilt binaries —
  no Go toolchain required at install time. The hand-maintained
  `Formula/loom.rb` (and its README) are removed; the tap formula is
  generated on each release.

## [1.1.0] — 2026-06-08 — Machine dev-state + UI

### Added

- `loom dev` — at-a-glance machine development state in three sections:
  a green/yellow/red loom health rollup (green = all daemons up and no
  sync backlog, yellow = backlog, red = a daemon down), projects with
  dirty working trees (changed-file counts), and projects with ready
  tickets. Color via lipgloss, degrades to plain text when piped.
- `loom dev` "Unreleased changelog" section — flags projects whose root
  `CHANGELOG.md` has entries under `[Unreleased]`, signalling a release
  is pending before those changes are published outside the repo.
- Candidate review screen actions: promote, reject, and edit a
  knowledge candidate inline.
- `loom ui` dashboard `(s)ort` — cycle the sort column, ordered
  left-to-right across the visible columns.

### Changed

- `loom tui` renamed to `loom ui`; the `tui` name stays as an alias so
  existing muscle memory and scripts keep working.

## [1.0.1] — 2026-04-28 — Distribution + auto-update

### Added

- `loom updater daemon` self-managing auto-updater. Polls
  `origin/main` on an interval, pulls + rebuilds + kickstarts every
  loom agent (itself last) when new commits land. Mirrors the
  Ghostwheel deployer pattern.
- Homebrew formula (`Formula/loom.rb`) and tap publish flow
  (`Formula/README.md`) so users can install the loom binary without a
  Go toolchain.
- `docs/auto-update-pattern.md` — reusable template for
  Go-binary-on-launchd apps that want the same poll-rebuild-kickstart
  loop.
- `CHANGELOG.md` (this file).

### Fixed

- `Formula/loom.rb` URL interpolation: `v#{version}` (Ruby) instead of
  the literal `v#…` that an earlier hand-edit left behind.

## [0.4.0] — 2026-04-28 — Wire-level project identity

### Added

- `wire.IngestRequest.ProjectIdentity{GitRemote, Cwd, RootSlug}` —
  authoritative project handle carried end to end. Optional;
  pre-identity clients omit it and the receiver tolerates absence.
- Receiver writes `<session>.meta.json` next to each `<session>.jsonl`
  so downstream readers (summarizer, TUI) recover identity without
  re-shipping.
- `summaries.db` schema v3: `sessions.git_remote` and
  `sessions.cwd_raw`, with an index on `git_remote`.
- TUI groups projects by `git_remote → cwd → slug`; basename
  collisions like `EnderRealm/loom` vs `elsewhere/loom` no longer
  collapse, while cross-machine clones of the same repo do.
- Receiver logs each ingest's identity tag (`identity=remote=…` /
  `identity=cwd=…` / `identity=slug-only` / `identity=none`) for live
  debugging.
- `REPO` column in the TUI dashboard showing the canonical
  `owner/repo` per project.
- `loom status` reports per-agent `interval` and `last activity`.
- Fixture tests: `transport/internal/source.TestReadClaudeCwd*` (nine
  cases pinning sparse-headered first-line scanning),
  `internal/tui.TestLoadProjectsIdentityGrouping` (synthetic
  cross-machine collapse).

### Fixed

- `readClaudeCwd` now scans up to 200 head lines for the first cwd
  field. Previously it only inspected the first record, which is
  often a sparse-headered (permission-mode, file-history-snapshot)
  entry with no cwd — sessions matching that shape shipped without
  identity and the TUI duplicated them per origin slug.
- `launchd.Install` polls for the prior service to disappear before
  bootstrapping and retries on transient EIO. Closes a race where
  bootout returned while launchd was still tearing the old job down,
  causing "Bootstrap failed: 5: Input/output error" on reinstall.
- Codex `Unknown`-record accounting: counts now reflect actual
  occurrences (was Count=0 on first sight, never refreshed).
- Both parsers stamp `UnknownRecord.FirstSeen` from the transcript's
  own timestamp instead of `time.Now()` — re-summarizing the same
  input now produces byte-identical output.
- Summarizer log lines now carry timestamps (`log.Printf` instead of
  `fmt.Printf`).
- TUI dashboard column headers no longer run together
  (`WORKTREESAGENTS` / `SESSIONSACTIVITY`).
- `loom status` deduplicates the `state =` line that
  `launchctl print` emits three times for resource and jetsam
  coalition blocks.

### Changed

- `summaries.db` primary key is `(agent, session_id)` end-to-end,
  matching the natural read-side join. Schema bumped to v2 in this
  release, then v3 with the identity columns.
- Schema bumps return `ErrSchemaOutdated` from `summaries.Open` and
  surface a `--rebuild` instruction. The summary DB is permanently
  disposable; `loom summarize --rebuild` drops and re-folds from
  `~/.loom/received/`.
- `notify.State.Maybe` takes `(enabled, cooldown)` scalars instead of
  a `*config.Config` so the notify package no longer pulls in a
  typed-config dependency.

## [0.3.0] — 2026-04-27 — Single binary + KeepAlive shipper

### Added

- One `loom` binary with `shipper`, `receiver`, `summarizer`, `tui`,
  `install`, `uninstall`, `status` subcommands. The four standalone
  binaries (`loom-shipper`, `loom-receiver`, `loom-summarize`,
  `loom-tui`) are deleted.
- `internal/launchd` package owns plist generation, `plutil -lint`,
  and `launchctl bootout/bootstrap/kickstart`. One generator covers
  all components.
- `loom shipper daemon` mode: `KeepAlive=true`,
  `ThrottleInterval=10`, in-process `time.Ticker` driven by
  `interval_minutes` from `config.json`. Replaces the
  `StartInterval`-based one-shot plist.

### Changed

- `loom install <component>` does build + plist + bootstrap + verify
  entirely in Go. No more shell-out to `install.sh`, no
  `repoRoot()` heuristic. `install.sh` reduced to a transitional
  forwarder.

### Fixed

- Shipper no longer relies on launchd's `StartInterval`, which dasd
  coalesces aggressively on modern macOS (we observed gaps of 24h+
  on a 10-minute schedule). The daemon owns its own cadence and
  launchd just respawns it on crash.

## [0.2.0] — 2026-04-28 — Drift telemetry correctness

### Added

- Composite `(agent, session_id)` primary key across every
  `summaries.db` table; the read model and the storage model now
  agree.
- `loom summarize --rebuild` flag: drops and re-folds the summary DB.
- Fixture-driven parser tests under
  `internal/parse/{claudeparse,codexparse}/testdata/`. Host-only
  smoke tests stay for breadth coverage but no longer load-bearing.

### Fixed

- Codex parser's `Unknown`-record accounting bug (copy-then-increment
  on first sight, slice never refreshed).
- `FirstSeen` reproducibility: parsers stamp from the transcript's
  own timestamp, not wall clock.
- README description matches reality (shipper + receiver + summarizer
  + TUI + unified CLI, two agents).

## [0.1.0] — Earlier 2026 — Pre-rewrite history

The pre-cleanup state. Multi-binary layout, slug-derived project
identity, `StartInterval`-based shipper, separate `cmd/loom-summarize`
/ `cmd/loom-tui` / `transport/cmd/loom-shipper` /
`transport/cmd/loom-receiver` binaries. Preserved here so anyone
spelunking through `git log` sees where the architecture started.
