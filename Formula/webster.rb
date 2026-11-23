class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.5.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.5.0/webster-macos-arm64.tar.gz"
    sha256 "712f5a0ec841574cf7c635160c7fed5e8ed360850e990bad27b8084e835ae0ff"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.5.0/webster-macos-amd64.tar.gz"
    sha256 "8bf929fa656c7810eeb5245d69a8d4ca8bc574bc4fa0805c786a921b39b30093"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
