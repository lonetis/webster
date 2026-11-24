class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.9.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.9.0/webster-macos-arm64.tar.gz"
    sha256 "28e81edfd3ffbd498045de9b8fddc81149b857650a39f1883f1e8bcaa4bfe71b"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.9.0/webster-macos-amd64.tar.gz"
    sha256 "8f2729cf1103f57aaba13038183af1fd335ddfdb544a15b679490d140f46ffc6"
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
