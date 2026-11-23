class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.7.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.7.0/webster-macos-arm64.tar.gz"
    sha256 "acfea2750f03a6148023f6480226603e8a4d04b36ed17c8a66791d1011019d21"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.7.0/webster-macos-amd64.tar.gz"
    sha256 "a4058c97e7e9b9884fd7b059d90c5e3bfc4a9ab11610c6b34f83bff7de3d527c"
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
