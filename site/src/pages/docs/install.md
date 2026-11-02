---
layout: ../../layouts/DocsLayout.astro
title: Install
---

# Install

tamesu is a single binary. Install it with Homebrew on macOS, Scoop on Windows, a `.deb`, `.rpm`
or `.apk` package on Linux, a release archive for any of the three, the Docker image, or
`go install`. Then run `tamesu doctor` to check your setup.

## Quick start

```sh
brew install xavidop/tap/tamesu
tamesu version
tamesu init && tamesu validate
```

## Platforms

| Platform | Package manager | Package | Release archive | From source |
|---|---|---|---|---|
| macOS (amd64, arm64) | Homebrew (cask) | no | yes | yes |
| Linux (amd64, arm64) | no | `.deb`, `.rpm`, `.apk` | yes | yes |
| Windows (amd64, arm64) | Scoop | no | yes (zip) | yes |

## Two builds

Each release has two builds of the same CLI:

- **`tamesu`** (the full build): everything, including audio mode, SIP and the Silero voice
  activity detector. Attached to the release as `tamesu_<version>_<os>_<arch>`.
- **`tamesu-lite`**: a single static binary. No audio mode and no SIP; everything else works. This
  is what the Homebrew cask, the Scoop manifest, the Linux packages and the Docker image install.

Both report the same version and install as `tamesu`.

## Homebrew (macOS)

```sh
brew install xavidop/tap/tamesu
tamesu version
```

This installs a Homebrew cask, so it works on **macOS only**. The cask clears the quarantine
attribute after install. On Linux, use a package or a release archive.

## Scoop (Windows)

```powershell
scoop bucket add xavidop https://github.com/xavidop/scoop-bucket
scoop install tamesu
tamesu version
```

Scoop installs the Windows zip from the release and keeps it up to date with `scoop update tamesu`.

## Linux packages

Each release attaches a `.deb`, an `.rpm` and an `.apk` for amd64 and arm64, named
`tamesu_<version>_linux_<arch>.<format>`. There is no package repository: download the file for
your distribution from the [GitHub releases page](https://github.com/xavidop/tamesu/releases) and
install it. For the latest release on amd64:

```sh
v=$(curl -fsSL https://api.github.com/repos/xavidop/tamesu/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
curl -fsSLO "https://github.com/xavidop/tamesu/releases/download/v$v/tamesu_${v}_linux_amd64.deb"
sudo dpkg -i "tamesu_${v}_linux_amd64.deb"      # Debian, Ubuntu
# sudo rpm -i "tamesu_${v}_linux_amd64.rpm"     # Fedora, RHEL, openSUSE
# sudo apk add --allow-untrusted "tamesu_${v}_linux_amd64.apk"   # Alpine
tamesu version
```

The package puts `tamesu` in `/usr/bin` and installs the bash, zsh and fish completions. The
packages are not signed, hence `--allow-untrusted` on Alpine; check the file against
`checksums.txt` from the same release if you want to verify it.

## Release archives (Linux, macOS, Windows)

Download the archive for your platform from the
[GitHub releases page](https://github.com/xavidop/tamesu/releases). Check it against
`checksums.txt` (`checksums-full.txt` for the full build), unpack it, and put the `tamesu`
binary on your `PATH`:

```sh
sha256sum --check --ignore-missing checksums.txt   # macOS: shasum -a 256 --check --ignore-missing checksums.txt
tar xzf tamesu_*_linux_amd64.tar.gz
install -m 0755 tamesu_*/tamesu ~/.local/bin/
tamesu version
```

Archives are `.tar.gz` on Linux and macOS and `.zip` on Windows, for amd64 and arm64. Each holds
the binary, the license, the README and the shell completions under `completions/`. Each release
also includes an SBOM per archive and SLSA provenance (`tamesu.intoto.jsonl`). The binaries are
not code-signed: on macOS, Gatekeeper may block a binary you download yourself until you clear
the quarantine attribute (`xattr -d com.apple.quarantine tamesu`). The Homebrew cask does this
for you.

## Docker

`ghcr.io/xavidop/tamesu` ships the lite CLI with Chromium preinstalled, for CI users of the
browser target:

```sh
docker run --rm -v "$PWD:/work" ghcr.io/xavidop/tamesu:latest validate
```

The working directory inside the image is `/work`; mount your project there. Pass secrets as
environment variables (`-e SUPPORT_API_TOKEN`) or mount a `.env` and use `--env-file`.

## Go

```sh
go install github.com/xavidop/tamesu/cmd/tamesu@latest
```

You need Go 1.27 or newer. This builds the lite variant on any platform; add `-tags onnx` to a
`go build` of `./cmd/tamesu` for the Silero detector. The binary reports its version as `(devel)` unless
built from a tag.

## From source

```sh
git clone https://github.com/xavidop/tamesu
cd tamesu && make build
```

The binary is written to `cmd/tamesu/tamesu`. A binary built this way reports its version as
`dev`; the release binaries and the packages report the release version. Windows has no `make`:
run `go build -o tamesu.exe .` inside `cmd/tamesu`.

## Shell completion

`tamesu completion <shell>` prints a completion script for bash, zsh, fish or PowerShell. The
Linux packages install it for bash, zsh and fish; after any other install, load it yourself:

```sh
# bash: once per machine (needs the bash-completion package)
tamesu completion bash > /etc/bash_completion.d/tamesu          # Linux
tamesu completion bash > $(brew --prefix)/etc/bash_completion.d/tamesu   # macOS, Homebrew bash-completion

# zsh: once, then start a new shell (`compinit` must be enabled in ~/.zshrc)
tamesu completion zsh > "${fpath[1]}/_tamesu"                  # Linux
tamesu completion zsh > $(brew --prefix)/share/zsh/site-functions/_tamesu   # macOS

# fish
tamesu completion fish > ~/.config/fish/completions/tamesu.fish
```

```powershell
# PowerShell: add to your profile
tamesu completion powershell | Out-String | Invoke-Expression
```

## Check the install

```sh
tamesu version          # version, commit and build date
tamesu doctor           # config, suites, every declared secret; no values printed
```

If anything reports `fail`, the line says what to fix; see also [Security](/docs/security/) for
how secrets are resolved.

## Next

Follow the [Quick start](/docs/quickstart/).
