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
   `-X loom/cmd/loom/cmd.Version={{.Version}}`.
2. Builds `loom_<version>_<os>_<arch>.tar.gz` archives plus a
   `checksums.txt`.
3. Publishes a GitHub Release with those archives as assets and a
   GitHub-native changelog generated from the commits in the tag range.
4. Commits the generated formula to the `EnderRealm/homebrew-tools`
   tap, so `brew install enderrealm/tools/loom` resolves to the new
   version.

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

The workflow needs one repository secret beyond the built-in
`GITHUB_TOKEN`:

- `TAP_GITHUB_TOKEN` — a PAT with write access to
  `EnderRealm/homebrew-tools`, used to commit the formula. The same
  token backs the `ticket` release pipeline.

## Auto-update vs. Homebrew

Source checkouts can instead run `loom install updater`, a daemon that
polls `origin/main`, rebuilds, and kickstarts the loom agents. That
path is independent of the Homebrew release flow and does not require a
tag — see the "Auto-update" section of the [README](../README.md).
