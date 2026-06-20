# Releasing loom

Releases are fully automated by [GoReleaser](https://goreleaser.com).
Cutting a release is a single action: **push a `vX.Y.Z` tag**.

```sh
git tag v1.2.0
git push origin v1.2.0
```

The `Release` workflow (`.github/workflows/release.yml`) fires on any
`v*` tag and runs `goreleaser release --clean`, which:

1. Cross-compiles the `loom` binary for darwin/linux × amd64/arm64
   (`CGO_ENABLED=0` — the build is pure Go via `modernc.org/sqlite`).
   The tag drives `loom --version` through
   `-X loom/internal/version.Current={{.Version}}`.
2. Builds `loom_<version>_<os>_<arch>.tar.gz` archives plus a
   `checksums.txt`.
3. Publishes a GitHub Release with those archives as assets and a
   GitHub-native changelog generated from the commits in the tag range.

Config lives in [`.goreleaser.yml`](../.goreleaser.yml). Validate
changes locally before pushing a tag:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish   # full dry-run into ./dist
```

## Version numbering

Versioning follows [SemVer](https://semver.org). Roll the
`[Unreleased]` section of [`CHANGELOG.md`](../CHANGELOG.md) into a dated
`[X.Y.Z]` entry in the same commit that you tag.

## Secrets

The workflow needs no secrets beyond the built-in `GITHUB_TOKEN`,
which is enough to publish the release and its artifacts.

## Releases are the deploy channel

A tagged release is the deploy unit. The `loom updater` daemon on each
machine polls the latest GitHub Release, and when a newer release than
the running binary ships it downloads the platform tarball, verifies its
`checksums.txt` entry, installs the extracted binary over
`~/.local/bin/loom`, and kickstarts the loom agents (see the
"Auto-update" section of the [README](../README.md)). Cutting a tag is
therefore what deploys code to the fleet; an unreleased commit on `main`
reaches a machine only when it ships in a release.
