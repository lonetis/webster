class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.6.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.6.0/webster-macos-arm64.tar.gz"
    sha256 "a018506fe1a453a625f7c663b9b9edd72d6ca5b896374024091ffe69bd3588ac"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.6.0/webster-macos-amd64.tar.gz"
    sha256 "421d418dfd96a58131f03f837d8663c003eebbfd1a31e12f5feb7d5a475d1792"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
