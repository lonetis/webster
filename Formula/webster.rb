class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.1.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.1.0/webster-macos-arm64.tar.gz"
    sha256 "e1f1c677608c22c6d28a5a51733e96775a7b0c14739cdb374293b6068ddbaf92"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.1.0/webster-macos-amd64.tar.gz"
    sha256 "5bf03f5f289779e5e0bdebe25afb8aacbd65311e1c3dcce11361218ff960ac64"
  end

  depends_on :macos

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
  end
end
