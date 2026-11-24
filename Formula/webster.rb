class Webster < Formula
  desc "Terminal connection tester for CrUX web origins"
  homepage "https://github.com/lonetis/webster"
  version "0.11.0"

  on_arm do
    url "https://github.com/lonetis/webster/releases/download/v0.11.0/webster-macos-arm64.tar.gz"
    sha256 "5c4f3fb6e7536de80efcd1e320ecba1369a564c9176541ce8dff91ff9964f9e0"
  end

  on_intel do
    url "https://github.com/lonetis/webster/releases/download/v0.11.0/webster-macos-amd64.tar.gz"
    sha256 "8215ca8afd4d9593e99ef63259118fd878a96ba1539654b8a0809e13c7b47a4a"
  end

  depends_on macos: :ventura

  def install
    bin.install "webster"
  end

  test do
    assert_match "Usage: webster", shell_output("#{bin}/webster -help 2>&1")
    assert_match "Usage: webster scan", shell_output("#{bin}/webster scan -help 2>&1")
    assert_match "HTTPS latency", shell_output("#{bin}/webster speedtest -help 2>&1")
    (testpath/"origins.csv").write "origin,rank\nhttp://127.0.0.1,1\n"
    assert_match "1 skipped", shell_output("#{bin}/webster scan -input origins.csv -dns-only -schedule fixed -concurrency 2")
  end
end
