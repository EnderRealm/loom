# Homebrew formula

`loom.rb` is the source of truth for the Homebrew build metadata.

## Publishing a new release

1. Tag the release on `main`:
   ```sh
   git tag v0.4.0
   git push origin v0.4.0
   ```
2. GitHub generates the source tarball at
   `https://github.com/EnderRealm/loom/archive/refs/tags/v0.4.0.tar.gz`.
   Compute its sha256:
   ```sh
   curl -sL https://github.com/EnderRealm/loom/archive/refs/tags/v0.4.0.tar.gz \
     | shasum -a 256
   ```
3. Update `version` and `sha256` in `loom.rb`.
4. Copy `loom.rb` into the tap repo (typically
   `github.com/EnderRealm/homebrew-loom` under `Formula/loom.rb`) and
   commit/push.

## User install

```sh
brew tap EnderRealm/loom
brew install loom
loom install server     # receiver + summarizer
loom install shipper    # client side
```

## Why a tap and not homebrew/core?

`homebrew/core` requires audit, stable API, and a ≥75-star GitHub
project. A personal tap has none of those constraints and is the
right home for tools maintained for an organization rather than the
public.
