# Homebrew formula template for the kaeawc/tap tap.
#
# The release workflow renders this file with scripts/release-formula.sh,
# replacing the at-sign placeholders with the tag and the SHA-256 of each
# release archive from checksums.txt, and attaches the result to the GitHub
# release as spectra.rb. Copying it into the tap is a manual maintainer step
# (see packaging/release/README.md); this template is not installable as is.
class Spectra < Formula
  desc "Host-aware app and JVM diagnostics (macOS + Linux)"
  homepage "https://github.com/kaeawc/spectra"
  version "@VERSION@"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/kaeawc/spectra/releases/download/@TAG@/spectra_@TAG@_darwin_arm64.tar.gz"
      sha256 "@SHA256_DARWIN_ARM64@"
    end
    on_intel do
      url "https://github.com/kaeawc/spectra/releases/download/@TAG@/spectra_@TAG@_darwin_amd64.tar.gz"
      sha256 "@SHA256_DARWIN_AMD64@"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/kaeawc/spectra/releases/download/@TAG@/spectra_@TAG@_linux_arm64.tar.gz"
      sha256 "@SHA256_LINUX_ARM64@"
    end
    on_intel do
      url "https://github.com/kaeawc/spectra/releases/download/@TAG@/spectra_@TAG@_linux_amd64.tar.gz"
      sha256 "@SHA256_LINUX_AMD64@"
    end
  end

  def install
    # The privileged helper is installed next to spectra but never enabled:
    # `sudo spectra install-helper` remains an explicit user step.
    bin.install "bin/spectra", "bin/spectra-mcp", "bin/spectra-helper"
    pkgshare.install "agent/spectra-agent.jar" if File.exist?("agent/spectra-agent.jar")
    (pkgshare/"systemd").install Dir["systemd/*.service"] if OS.linux?
    doc.install Dir["docs/*.md"]
    prefix.install "LICENSE"
    prefix.install "README.md" if File.exist?("README.md")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/spectra version")
    assert_match version.to_s, shell_output("#{bin}/spectra-mcp --version")
    assert_match version.to_s, shell_output("#{bin}/spectra-helper --version")
  end
end
