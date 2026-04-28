# Homebrew formula for loom.
#
# This file lives in the loom repo so the source of truth for build
# metadata travels with the code. To publish, copy it into a tap repo
# (typically github.com/EnderRealm/homebrew-loom under Formula/loom.rb)
# and bump `url`/`sha256`/`version` per release.
#
#     brew tap EnderRealm/loom
#     brew install loom
#
# After install, set up launchd agents:
#
#     loom install server     # on the receiver host
#     loom install shipper    # on each client host (needs ~/.loom/config.json)
#     loom install updater    # optional: source-checkout users only
#
class Loom < Formula
  desc "Capture, ship, summarize, and explore agent session transcripts"
  homepage "https://github.com/EnderRealm/loom"
  license "MIT"

  # Bump per release. The release tarball is the standard GitHub
  # archive; sha256 is `shasum -a 256` of that tarball.
  version "1.0.1"
  url "https://github.com/EnderRealm/loom/archive/refs/tags/v#{version}.tar.gz"
  sha256 "REPLACE_WITH_RELEASE_SHA256"

  depends_on "go" => :build

  def install
    # Build the single loom binary. ldflags bake the version into
    # `loom --version` so users can tell what they have installed.
    ldflags = ["-s", "-w", "-X", "loom/cmd/loom/cmd.Version=#{version}"]
    system "go", "build",
           "-trimpath",
           "-ldflags", ldflags.join(" "),
           "-o", bin/"loom",
           "./cmd/loom"
  end

  test do
    # `loom --help` prints to stdout and exits zero on a healthy build.
    assert_match "Usage: loom", shell_output("#{bin}/loom --help")
    assert_match version.to_s, shell_output("#{bin}/loom --version")
  end
end
