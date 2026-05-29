class Vtunnel < Formula
  desc "Pleasant local tunnels powered by Cloudflare Tunnel"
  homepage "https://github.com/vltn-sh/vtunnel"
  version "0.0.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/vltn-sh/vtunnel/releases/download/v0.0.0/vtunnel_v0.0.0_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_ARM64_SHA256"
    else
      url "https://github.com/vltn-sh/vtunnel/releases/download/v0.0.0/vtunnel_v0.0.0_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_AMD64_SHA256"
    end
  end

  depends_on "cloudflared"

  def install
    bin.install "vtunnel"
  end

  def caveats
    <<~EOS
      To start using vtunnel, run:
        vtunnel onboarding
    EOS
  end

  test do
    assert_match "vtunnel", shell_output("#{bin}/vtunnel --version")
  end
end
