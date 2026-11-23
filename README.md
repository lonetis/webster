# Webster

Webster is a terminal connection tester for Chrome User Experience Report (CrUX) origins. Choose a target count and worker count, watch HTTP responses and network failures arrive, and adjust concurrency or timeouts during a run.

## Install

### Ubuntu

#### Per-user install without sudo

Extract the release for your CPU into `~/.local/bin`. Run the same commands to upgrade.

```sh
(
  set -e
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  arch=$(dpkg --print-architecture)
  curl -fsSL "https://github.com/lonetis/webster/releases/latest/download/webster-linux-$arch.deb" -o "$tmp/webster.deb"
  dpkg-deb -x "$tmp/webster.deb" "$tmp/pkg"
  install -Dm755 "$tmp/pkg/usr/bin/webster" "$HOME/.local/bin/webster"
)
export PATH="$HOME/.local/bin:$PATH"
```

If a new shell cannot find `webster`, add the `export PATH` line to your shell profile.

#### System-wide install with APT

Download the package for your CPU and install it with APT. The release provides `amd64` and `arm64` packages.

```sh
curl -fL -o webster.deb "https://github.com/lonetis/webster/releases/latest/download/webster-linux-$(dpkg --print-architecture).deb" &&
  sudo apt install ./webster.deb
```

Run the same commands to upgrade to a newer release.

### macOS

With Homebrew installed, run these commands as your regular user on Apple Silicon or Intel Macs running macOS 13 or newer. Installing and removing Webster with Homebrew requires no `sudo`.

```sh
brew tap lonetis/webster https://github.com/lonetis/webster
brew install lonetis/webster/webster
```

To upgrade, run `brew update && brew upgrade lonetis/webster/webster`.

## First run

```sh
webster fetch
webster
```

The first command downloads the latest global CrUX snapshot to `~/.local/share/webster/data/` and checks the byte and origin counts of every chunk. Allow about 650 MiB of disk space per monthly snapshot. Running `webster fetch` again reuses a complete copy of the latest month or downloads the new month. [CrUX Cache](https://lonetis.github.io/crux-cache/)

Press **Enter** to check the first 1,000 origins in rank order with the default settings. To try just a handful, press **n**, enter a count, and press **Enter** to apply it before starting.

## Uninstall

**Ubuntu per-user install**

```sh
rm "$HOME/.local/bin/webster"
```

**Ubuntu APT install**

```sh
sudo apt remove webster
```

**macOS**

```sh
brew uninstall lonetis/webster/webster
brew untap lonetis/webster
```

**Earlier per-user installs** placed a launcher in `~/.local/bin`. If you installed that version, remove its executable and launchers with the following command. You can also use this when switching to the package-manager version to make your shell pick up the new executable.

```sh
rm -f "$HOME/.local/bin/webster" "$HOME/.local/share/webster/launcher" "$HOME/.local/share/webster/webster"
```

To reclaim the downloaded snapshot space, delete `~/.local/share/webster/data/`. Versions through 0.3.0 also saved CSVs in `~/.local/share/webster/results/`. You can remove that directory separately if you want to discard those historical runs.

## Controls

| Key | Action |
| --- | --- |
| ← / → | Choose 1k, 10k, 100k, 1m, or all origins before starting |
| n | Enter an exact target count before starting |
| + / - | Increase or decrease the process count |
| p | Enter an exact process count |
| [ / ] | Decrease or increase the timeout by one second |
| t | Enter a timeout in whole seconds |
| d | Cycle through system, recursive, and iterative DNS before or during a run |
| v | Cycle through the recursive endpoints, all four, and any subset supplied at startup |
| r | Open live resolver statistics during a scan or after completion. Use ↑ / ↓ to inspect an endpoint |
| Enter | Apply an edited value or start from the setup screen |
| Esc | Cancel an edit, or quit from the dashboard |
| q / Ctrl+C | Cancel pending checks, print the totals, and quit |

The defaults are 1,000 targets, eight OS worker processes, and a 10-second deadline per request. Each worker handles one origin at a time. Increasing the pool starts more workers. Decreasing it lets excess workers finish their current requests and retire. Timeout changes apply to the next assignment and cover DNS, connection, TLS, and response headers.

## DNS resolution

System DNS is the default. The packaged binary includes ZDNS for recursive and iterative lookups. Press **d** to change modes during setup or a scan. New assignments pick up the change immediately. In-flight checks finish with their original settings. [ZDNS](https://github.com/zmap/zdns)

| Start Webster with | How it resolves origins |
| --- | --- |
| `webster -dns system` | Use the operating system resolver |
| `webster -dns recursive -provider cloudflare` | Ask Cloudflare at `1.1.1.1` to resolve the name |
| `webster -dns recursive -provider google` | Ask Google at `8.8.8.8` to resolve the name |
| `webster -dns recursive -provider all` | Spread successive origins across all four Cloudflare and Google endpoints |
| `webster -dns recursive -provider cloudflare,google` | Alternate between the two primary endpoints |
| `webster -dns iterative` | Start with ZDNS's built-in root hints and follow referrals to authoritative servers |

Recursive mode defaults to Cloudflare. With `-provider all`, Webster assigns origins to `1.1.1.1`, `8.8.8.8`, `1.0.0.1` (`cloudflare-alt`), and `8.8.4.4` (`google-alt`) in turn across the entire worker pool. A comma-separated subset uses the order you give it. Each origin sends its A and AAAA lookups and any retries to its assigned endpoint. Press **v** to change the selection during a scan and **r** to see each endpoint’s replies, refusals, and timeouts. [Cloudflare addresses](https://developers.cloudflare.com/1.1.1.1/ip-addresses/) · [Google addresses](https://developers.google.com/speed/public-dns/docs/using)

Both ZDNS modes look up IPv4 and IPv6 addresses, follow aliases, and connect to the resulting IPs using the original HTTP hostname and TLS certificate checks. DNS traffic uses IPv4 and requires outbound DNS on port 53, including TCP for truncated UDP responses. Iterative mode needs access to root and authoritative servers. IP-literal origins skip DNS. Workers keep a bounded DNS answer cache in memory for the run.

### Live resolver status

Press **r** to see the recursive endpoints and an aggregate row for iterative lookups. Each A or AAAA lookup produces one outcome, including any retries and referrals. **R/s** is lookup replies per second, **Reply%** is the fraction of completed uncached lookups that returned a DNS response code, and **Avgms** is the average elapsed time for those replies. These columns cover the last ten seconds. Fully cached lookups have their own counter. System DNS and IP literals produce no ZDNS samples.

Select a row with **↑ / ↓** to see pending lookups, refusals, policy responses, timeouts, and other errors. **Ref** counts `REFUSED` outcomes. **EDE** counts results carrying an explicit extended DNS error for blocking, censorship, filtering, or prohibition. Both can count as replies. A timeout indicates a missing response and cannot establish why it was lost. [Extended DNS Errors](https://www.rfc-editor.org/rfc/rfc8914.html)

## Results

Each check sends a single `HEAD /` with certificate validation and records the origin's own HTTP status. Redirects, 4xx, and 5xx all count as reachable. Failed checks include a reason and error detail.

The dashboard shows live reachable and failed checks per second, the six most frequent HTTP status codes, counts by failure reason, and recent origins. The process counter shows active versus requested workers.

Scan results stay in memory. Webster creates no result files, logs, or output directories. On quit it cancels pending checks and prints only the completed, reachable, and failed totals. Webster accepts and ignores the legacy `-results` flag.

To scan without downloading anything, use `webster -input origins.csv` with an existing CSV whose header is `origin,rank`. The separate `webster fetch` command writes the input snapshot. Set its directory with `-data` on both commands.

## Build from source

Use Go 1.25 or newer. Apple Silicon source builds also need the Xcode Command Line Tools for cgo. From a checkout, run the following commands.

```sh
go build -o webster .
./webster fetch
./webster
```

Source builds read snapshots from `./data/` by default.
