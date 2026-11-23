class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.8.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.8.0/webster-macos-arm64.tar.gz"
    sha256 "6bf7a2a204684b1545a65f9c1e593bc6cbf9cafe25c7b4ac4cf24ef89d043d21"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.8.0/webster-macos-amd64.tar.gz"
    sha256 "37b1af7f9dc182f58e2187a91bb9a48505e40fb854b3728a9e17888b2e039d43"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
    assert_match "HTTPS latency", shell_output("#{bin}/webster speedtest -help 2>&1")
  end
end
