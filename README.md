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

With Homebrew installed, run these commands as your regular user on Apple Silicon or Intel Macs. Installing and removing Webster with Homebrew requires no `sudo`.

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

Snapshots and result CSVs belong to your user account. To reclaim the snapshot space, delete `~/.local/share/webster/data/`. To also discard saved runs, delete `~/.local/share/webster/results/`.

## Controls

| Key | Action |
| --- | --- |
| ← / → | Choose 1k, 10k, 100k, 1m, or all origins before starting |
| n | Enter an exact target count before starting |
| + / - | Increase or decrease the process count |
| p | Enter an exact process count |
| [ / ] | Decrease or increase the timeout by one second |
| t | Enter a timeout in whole seconds |
| d | Toggle system DNS and rotating DNS before or during a run |
| Enter | Apply an edited value or start from the setup screen |
| Esc | Cancel an edit, or quit from the dashboard |
| q / Ctrl+C | Quit immediately and keep recorded results |

The defaults are 1,000 targets, eight OS worker processes, and a 10-second deadline per request. Each worker handles one origin at a time. Increasing the pool starts more workers. Decreasing it lets excess workers finish their current requests and retire. Timeout changes apply to the next assignment and cover DNS, connection, TLS, and response headers.

## DNS resolution

Webster uses system DNS by default. Press **d** or start with `webster -dns rotating` to use public resolvers for origin checks. In rotating mode, successive origins are assigned to `1.1.1.1`, `8.8.8.8`, and `9.9.9.9` in round-robin order across all workers. Resolver policies can affect results. For example, Quad9 applies threat blocking at `9.9.9.9`. [Cloudflare](https://developers.cloudflare.com/1.1.1.1/ip-addresses/) · [Google Public DNS](https://developers.google.com/speed/public-dns/docs/using) · [Quad9](https://quad9.net/service/service-addresses-and-features/)

Supply a comma-separated list to choose your own rotation. IPv4 and IPv6 addresses use port 53 by default. To specify a port, use `IP:port` for IPv4 or `[IPv6]:port` for IPv6.

```sh
webster -dns rotating -resolvers 1.1.1.1,8.8.8.8
```

Each assignment uses one DNS mode for its entire request, including DNS retries. Press **d** again to switch back to system DNS for new assignments. IP-literal origins connect directly. HTTPS checks use the original hostname for TLS and certificate verification.

## Results

Each check sends a single `HEAD /` with certificate validation and records the origin's own HTTP status. Redirects, 4xx, and 5xx all count as reachable. Failed checks include a reason and error detail.

The dashboard shows live reachable and failed checks per second, the six most frequent HTTP status codes, counts by failure reason, and recent origins. The process counter shows active versus requested workers.

Webster writes a unique CSV for each run and prints its path on exit. Packaged installations save it under `~/.local/share/webster/results/`. Each row includes the origin, CrUX rank, outcome, status code, failure reason, error detail, elapsed milliseconds, and the assigned `dns_resolver` (`system` or a resolver endpoint). Quitting cancels outstanding requests.

Use `webster -input origins.csv` for a local CSV with the header `origin,rank`. Set the snapshot directory with `-data` and the output directory with `-results`.

## Build from source

With Go 1.24 or newer, run these commands from a checkout.

```sh
go build -o webster .
./webster fetch
./webster
```

Source builds default to `./data/` and `./results/` in the working directory.
