class Vtunnel < Formula
  desc "Pleasant local tunnels powered by Cloudflare Tunnel"
  homepage "https://github.com/valent1d/vtunnel"
  version "0.1.0-beta.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/valent1d/vtunnel/releases/download/v0.1.0-beta.1/vtunnel_v0.1.0-beta.1_darwin_arm64.tar.gz"
      sha256 "36b141d4da3e4c5249e6153724303da0414bce0e949e3a7b9badd78547feeda3"
    else
      url "https://github.com/valent1d/vtunnel/releases/download/v0.1.0-beta.1/vtunnel_v0.1.0-beta.1_darwin_amd64.tar.gz"
      sha256 "418784d50005a8b5ab371ab819946e271b345bc46352be426434e74cec87f462"
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
