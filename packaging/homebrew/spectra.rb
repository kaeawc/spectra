class Spectra < Formula
  desc "macOS app diagnostics and JVM-aware remote debugging"
  homepage "https://github.com/kaeawc/spectra"
  url "https://github.com/kaeawc/spectra/archive/refs/tags/v0.0.0.tar.gz"
  sha256 "REPLACE_WITH_SOURCE_ARCHIVE_SHA256"
  license "MIT"
  head "https://github.com/kaeawc/spectra.git", branch: "main"

  depends_on "go" => :build

  def install
    version_ldflag = "-X main.version=#{version}"
    system "go", "build", "-trimpath", "-ldflags", "-s -w #{version_ldflag}", "-o", bin/"spectra", "./cmd/spectra"
    system "go", "build", "-trimpath", "-ldflags", "-s -w #{version_ldflag}", "-o", bin/"spectra-mcp", "./cmd/spectra-mcp"
    system "go", "build", "-trimpath", "-ldflags", "-s -w #{version_ldflag}", "-o", bin/"spectra-helper", "./cmd/spectra-helper"

    pkgshare.install "agent/spectra-agent.jar" if File.exist?("agent/spectra-agent.jar")
    doc.install "docs/install.md", "docs/operations/install-services.md", "docs/design/distribution.md"
  end

  service do
    run [opt_bin/"spectra", "serve"]
    keep_alive true
    log_path var/"log/spectra.log"
    error_log_path var/"log/spectra.err.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/spectra version")
    assert_match version.to_s, shell_output("#{bin}/spectra-helper --version")
  end
end
