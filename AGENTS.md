# AGENTS.md

This file provides guidance to coding agents (Claude Code, Codex, Cursor) when working with code in this repository. `CLAUDE.md` is a symlink to it.

## Project Overview

Loom captures agent session transcripts, ships them to a central receiver, folds them into a normalized summary database, and surfaces raw state and durable knowledge through an interactive TUI. `docs/overview.md` covers the philosophy; `README.md` is the operator reference (install, server/client setup, state locations, commands, troubleshooting).

Subsystems, all driven by the single `loom` binary:

- **transport** — the shipper (client) captures Claude Code, Codex CLI and Cursor CLI sessions and ships them to the receiver (server).
- **summarizer** — folds `~/.loom/received/` into `~/.loom/summaries.db` (sqlite). The DB is a disposable derived artifact: there are no migrations; a schema bump is handled by `loom summarize --rebuild`.
- **extractor** — runs `extractors/extract.py` over newly summarized sessions against the durable knowledge store at `~/.loom/knowledge/` (its own git repo).
- **tui** — `loom ui`, the dashboard over projects, sessions and knowledge.

## Repo layout

```
cmd/loom/        the only binary; cobra subcommands
internal/        one package per concern: parse (transcript parsers + sqlite store), summarize,
                 summaries, extract, knowledge, synthesis, runs, runreport, workreport, friction,
                 escapes, pricing, pipeline, tui, launchd, updater, config, version
transport/       shipper/, receiver/, internal/ (wire, source, staging, notify)
extractors/      Python project for truth/decision extraction (stdlib unittest)
docs/            design and contract docs for each subsystem
knowledge/       eval fixtures only — the durable store is ~/.loom/knowledge/
```

## Build and test

```sh
make dev                    # go build -o ./loom ./cmd/loom — local build, independent of the installed binary
go test ./...               # Go suite
make test-extractors        # extractor suite, offline
make test-extractors-yaml   # strict-YAML extractor test; fetches PyYAML via uv
loom summarize --force --strict   # full sweep; exits non-zero if any session errored
```

The installed binary at `~/.local/bin/loom` is what the launchd agents run, and the updater keeps it on the latest release. Use `./loom` for development; overwrite the installed binary only for deliberate live testing (README "Testing an unreleased build"), and expect the updater to reinstate the release on its next tick.

For `verify:` criteria, `ticket_verify` runs one argv with no shell, so the exit code is the only signal: use `loom summarize --force --strict` for sweeps and the `make` targets above for the extractor suite (README "Verifying a sweep from a ticket").

## Key constraints

- **Wire protocol has no versioning.** `transport/internal/wire` is imported by both shipper and receiver; a shape change to `IngestRequest`/`IngestResponse` must land on both sides together.
- **Knowledge store writes go through one Go entry point** that commits and pushes what it wrote — never write `~/.loom/knowledge/` directly. See `docs/knowledge-store-writes.md`.
- **Transcript text is untrusted.** What extraction strips, where text may flow and what consumers may trust it for: `docs/transcript-trust-and-redaction.md`.
- **Release is deploy.** A `vX.Y.Z` tag triggers GoReleaser and the updater rolls it to every machine; an untagged commit on `main` reaches no machine. See `docs/releasing.md`.

## Changelog

Notable code changes add bullets under `## [Unreleased]` in `CHANGELOG.md` (Keep a Changelog headings: Added, Changed, Fixed, Removed; an `Upgrade` note when summaries.db schema or install steps change) in the same commit. A release commit (`Release X.Y.Z`) rolls `[Unreleased]` into a dated `[X.Y.Z]` entry; then tag and push the tag.

## Tickets

This project uses **tk** for task tracking — drive it via its MCP tools, never read or
edit the ticket store files directly. The model itself — statuses, types, hierarchy,
tools — comes from the `Tickets: the tk mental model` section of warp's `AGENTS.md`,
rendered into each agent's global instructions; if that section isn't in yours, go get
it rather than working from a guessed model. Work this repository delivers is ticketed in
this project even when its parent epic lives in another project or in Root (`_root`),
and it runs here, never in the epic's project.

## Skills

- `/capture`     — create a well-formed ticket (why + success contract).
- `/work`        — drive a ticket from open to done (implement → review loop → commit → push).
- `/brainstorm`  — refine a rough idea into a concrete design, then hand off to /capture.
- `/investigate` — disciplined debugging when the cause isn't obvious.

## Git hygiene

One ticket = one commit = one push. Commit messages reference the ticket id as
`[<ticket-id>]`. Not done until committed and pushed.
