class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.10.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.10.0/webster-macos-arm64.tar.gz"
    sha256 "6c370e4a7b2b982d7a9c895b0c9d2f0a1cfaf4e4fbf1ad02bba6c345e8f85104"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.10.0/webster-macos-amd64.tar.gz"
    sha256 "31fd341480a452774f2de307b1b3a1529151c0b823ed33fa766d1ace54a849b3"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
    assert_match "Usage: webster scan", shell_output("#{bin}/webster scan -help 2>&1")
    assert_match "HTTPS latency", shell_output("#{bin}/webster speedtest -help 2>&1")
  end
end
