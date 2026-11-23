class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.4.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.4.0/webster-macos-arm64.tar.gz"
    sha256 "3d1e7860bdf4c0d52dd39e9bda35bfa31102f3fe9dd2110011cb1b87f338822d"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.4.0/webster-macos-amd64.tar.gz"
    sha256 "eb4a8c2d2b3392e34616d4814e307649f0b756282f7753e3bafba16001fd5561"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
