class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.3.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.3.0/webster-macos-arm64.tar.gz"
    sha256 "b9478ecf217408e8e1baa76fb2cc87eca505057dfab8c8fc2f7d728ef36d4e77"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.3.0/webster-macos-amd64.tar.gz"
    sha256 "c5ee0dfbdf9975d12d0511efb05a6709af60c2877e656bf35689f235b38bcb3a"
  end

  depends_on :macos

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
