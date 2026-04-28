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
  version "1.0.0"
  url "https://github.com/EnderRealm/loom/archive/refs/tags/v#1.0.0.tar.gz"
  sha256 "0019dfc4b32d63c1392aa264aed2253c1e0c2fb09216f8e2cc269bbfb8bb49b5"

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
