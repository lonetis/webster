# Webster

Webster is a terminal connection tester for Chrome User Experience Report (CrUX) origins. Start an HTTP or DNS measurement from the shell, or open the interactive dashboard to watch origin results and resolver statistics together.

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

### Run a measurement from the shell

`webster scan` starts immediately with the same defaults as the dashboard. Supply measurement flags directly or reuse them from a JSON file.

```sh
webster scan -targets 100 -processes 4 -timeout 5s
webster scan -input origins.csv -targets all -dns-only -dns recursive -provider all
```

To reuse settings, save a file such as `office.json` with the options you need.

```json
{
  "targets": 1000,
  "processes": 8,
  "timeout": "5s",
  "dns-only": true,
  "dns": "recursive",
  "provider": "cloudflare,google"
}
```

```sh
webster scan -config office.json                # start immediately
webster scan -config office.json -targets 25    # override one setting
webster -config office.json                     # review settings in the dashboard
```

Explicit flags override the file. Both interfaces accept `targets` (a positive count, `"all"` or `0`), `processes`, `concurrency`, `schedule`, `timeout` (whole seconds), `dns-only`, `dns`, `provider`, `input` and `data`. Relative paths resolve from the working directory. Run `webster scan -help` for defaults.

The CLI writes settings and progress to stderr. Stdout contains the final counts, HTTP statuses, failure reasons, observed resolver counters and elapsed time. An unreachable origin is a measurement result. Input or worker errors give a nonzero exit status. **Ctrl+C** stops workers, prints partial totals and exits with a nonzero status.

### Memory, processes and scheduling

Webster reads the entire origin column into RAM and closes the CSV before starting measurements. The dashboard reuses that list when you start another run. `-targets` selects a prefix in file order. Scan timers start after loading. Budget RAM for the whole source even when selecting a small target count.

`-processes` sets the number of OS worker processes. `-concurrency` sets the maximum simultaneous origin checks **per process**. For example, this runs up to 16,000 checks across eight processes:

```sh
webster scan -input origins.csv -targets all -processes 8 -concurrency 2000 -schedule fixed
```

| Schedule | How work is assigned | During the run |
| --- | --- | --- |
| `dynamic` (default) | The parent hands out rows from RAM as workers free up slots. Faster processes can take more rows. | The dashboard can increase or decrease the process count. Retiring workers finish their assigned checks. |
| `fixed` | The parent divides the selected rows into contiguous partitions differing by at most one row and sends one to each process. Each process advances through its own partition independently. | The process count is locked. A process exits after finishing its partition, so the slowest partition determines when the run ends. |

Fixed workers hold their own partition in RAM in addition to the parent's list. Webster starts only workers with assigned origins. Both modes stream results and DNS observations back to the dashboard, and support stopping early. Concurrency per process is selected at setup. A high concurrency setting also requires enough file descriptors and memory for the active sockets.

### Internet speed test

Run `webster speedtest` from the shell, even before downloading a CrUX snapshot. In the dashboard, press **s** to run the same test. Results appear inline. Press **Esc** to cancel or close the speed test. To measure the connection while idle, run the test before starting a scan. During a scan, both share the connection.

By default Webster uses **four parallel TCP connections for 15 seconds per direction**, with a separate warm-up before download and upload. On a symmetric 10 Gbit/s connection, the two measurement windows alone can transfer about **37.5 GB**, plus warm-up, in-flight transfers, and protocol overhead. For a longer run with eight connections, use either command below.

```sh
webster speedtest -duration 30s -streams 8
webster -speed-duration 30s -speed-streams 8  # applies when you press s in the TUI
```

Webster reports the median of three warmed HTTPS round trips. For throughput, it keeps all connections transferring, adapting the request size on each connection. At the end of each window it lets outstanding transfers finish. The reported Mbps is the total verified payload divided by the elapsed wall time for that direction, including those final transfers. Each result shows the byte total and elapsed seconds. This measures the path to Apple’s network-quality service on both macOS and Ubuntu. Uploads use generated bytes and all results stay in memory. [Network Quality service documentation](https://github.com/network-quality/goresponsiveness#run)

### DNS-only checks

```sh
webster -dns-only
webster -dns-only -dns recursive -provider all
webster -dns-only -dns iterative
```

DNS-only mode performs one logical IPv4 address (A) lookup for each origin hostname and finishes before opening a connection to the origin. Select it with `-dns-only` or press **m** on the setup screen. The dashboard counts a hostname as **resolved** when the lookup returns an IPv4 address. IPv6-only names have no A record. Origins containing an IP address are **skipped**.

Recursive DNS-only checks use the first outcome from the assigned endpoint. Cached answers can avoid a wire query, and aliases, referrals, or TCP fallback can require additional DNS exchanges. System mode also honors local host records. The dashboard shows live ZDNS statistics alongside the origin results for recursive and iterative lookups.

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
| ↑ / ↓ | Select a setup field, or inspect a resolver during and after a scan |
| ← / → | Change the selected setup field. Target presets are 1k, 10k, 100k, 1m, and all |
| n | Enter an exact target count before starting |
| m | Switch between HTTP HEAD and DNS-only (A) checks before starting |
| + / - | Increase or decrease the process count during setup or a dynamic run |
| p | Enter an exact process count during setup or a dynamic run |
| c | Set the maximum concurrent checks per process during setup |
| f | Switch between dynamic and fixed scheduling during setup |
| [ / ] | Decrease or increase the timeout by one second |
| t | Enter a timeout in whole seconds |
| d | Cycle through system, recursive, and iterative DNS before or during a run |
| v | Cycle through the recursive endpoints, all four, and any subset supplied at startup |
| x | Stop the active scan and inspect partial results |
| PgUp / PgDn | Scroll when more measurements or settings extend beyond the screen. Home / End jumps to the top or bottom |
| s | Run the internet speed test, or repeat a finished test |
| Enter | Apply an edited value, start from setup, or return to setup after a finished run |
| Esc | Cancel an edit, close the inline speed test, or quit from the dashboard |
| q / Ctrl+C | Cancel pending checks, print the totals, and quit |

The defaults are 1,000 targets, eight OS processes, one concurrent check per process, dynamic scheduling and a 10-second deadline per request. Timeout changes apply to newly assigned checks in dynamic mode and to new checks after a fixed worker receives the update. The deadline covers DNS, connection, TLS and response headers.

## DNS resolution

System DNS is the default. The packaged binary includes ZDNS for recursive and iterative lookups. Press **d** to change modes during setup or a scan. Dynamic workers receive the new settings with new assignments. Fixed workers use the update as they start further checks in their partition. In-flight checks finish with their original settings. [ZDNS](https://github.com/zmap/zdns)

| Start Webster with | How it resolves origins |
| --- | --- |
| `webster -dns system` | Use the operating system resolver |
| `webster -dns recursive -provider cloudflare` | Ask Cloudflare at `1.1.1.1` to resolve the name |
| `webster -dns recursive -provider google` | Ask Google at `8.8.8.8` to resolve the name |
| `webster -dns recursive -provider all` | Spread successive origins across all four Cloudflare and Google endpoints |
| `webster -dns recursive -provider cloudflare,google` | Alternate between the two primary endpoints |
| `webster -dns iterative` | Start with ZDNS's built-in root hints and follow referrals to authoritative servers |

Recursive mode defaults to Cloudflare. With `-provider all`, Webster assigns origins to `1.1.1.1`, `8.8.8.8`, `1.0.0.1` (`cloudflare-alt`), and `8.8.4.4` (`google-alt`) in turn across the entire worker pool. A comma-separated subset uses the order you give it. Each origin sends its address lookups and any retries to its assigned endpoint. Press **v** to change the selection during a scan. The resolver table shows each endpoint’s replies, refusals, and timeouts. [Cloudflare addresses](https://developers.cloudflare.com/1.1.1.1/ip-addresses/) · [Google addresses](https://developers.google.com/speed/public-dns/docs/using)

For HTTP checks, both ZDNS modes look up IPv4 and IPv6 addresses, follow aliases, and connect to the resulting IPs using the original HTTP hostname and TLS certificate checks. DNS traffic uses IPv4 and requires outbound DNS on port 53, including TCP for truncated UDP responses. Iterative mode needs access to root and authoritative servers. IP-literal origins skip DNS. Workers keep a bounded DNS answer cache in memory for the run.

### Live resolver status

The dashboard shows the four recursive endpoints and an aggregate row for iterative lookups. Use **↑ / ↓** to select a row. On narrow terminals the resolver table appears below the throughput graph. Each A or AAAA lookup produces one outcome, including any retries and referrals. **R/s** is lookup replies per second, **Reply%** is the fraction of completed uncached lookups that returned a DNS response code, and **Avgms** is the average elapsed time for those replies. These columns cover the last ten seconds. Fully cached lookups have their own counter. System DNS and IP literals produce no ZDNS samples.

Select a row with **↑ / ↓** to see pending lookups, refusals, policy responses, timeouts, and other errors. **Ref** counts `REFUSED` outcomes. **EDE** counts results carrying an explicit extended DNS error for blocking, censorship, filtering, or prohibition. Both can count as replies. A timeout indicates a missing response and cannot establish why it was lost. [Extended DNS Errors](https://www.rfc-editor.org/rfc/rfc8914.html)

## Results

Each HTTP check sends a single `HEAD /` with certificate validation and records the origin's own HTTP status. Redirects, 4xx, and 5xx all count as reachable. Failed checks include a reason and error detail.

The dashboard shows successful and failed checks per second, the six most frequent HTTP status codes and failure reasons, and recent origins. The process counter shows active versus requested workers.

The dashboard and CLI show average origins/s for the whole run and origins/s over the last five seconds. During the first five seconds both use the elapsed run time. Each completed origin counts once toward throughput, including skipped DNS-only IP literals. Success and failure percentages use the completed, non-skipped checks as their denominator. A dash means no such check has finished yet.

Scan results stay in memory. Webster creates no result files, logs, or output directories. On quit it cancels pending checks and prints the totals for the selected check. Webster accepts and ignores the legacy `-results` flag.

To scan without downloading anything, use `webster -input origins.csv` with an existing CSV whose header is `origin,rank`. The separate `webster fetch` command writes the input snapshot. Set its directory with `-data` on both commands.

## Build from source

Use Go 1.25 or newer. Apple Silicon source builds also need the Xcode Command Line Tools for cgo. From a checkout, run the following commands.

```sh
go build -o webster .
./webster fetch
./webster
```

Source builds read snapshots from `./data/` by default.
