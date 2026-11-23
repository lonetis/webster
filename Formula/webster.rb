class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.2.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.2.0/webster-macos-arm64.tar.gz"
    sha256 "2fcdcee66b00267932af50b72e7ba31981d4d7d8b57e1916b0a0fd08d91e8c16"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.2.0/webster-macos-amd64.tar.gz"
    sha256 "d9e2f81e90e959f72ebf5e690578800e76722036caac8a9d154b21ed222a319c"
  end

  depends_on :macos

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
