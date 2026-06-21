---
title: "Installation"
description: "Install the vec binary with Homebrew, Scoop, apt or dnf, go install, Docker, or a prebuilt archive, or add the Go library to a project."
weight: 20
---

vec ships as a single static binary with no runtime dependencies.
Pick whichever channel matches your platform.
To embed vec in a Go program instead, skip to [the library](#the-library).

## Homebrew (macOS and Linux)

```bash
brew install tamnd/tap/vec
```

## Scoop (Windows)

```powershell
scoop bucket add tamnd https://github.com/tamnd/scoop-bucket
scoop install vec
```

## apt and dnf (Linux)

The release publishes `.deb` and `.rpm` packages to the tamnd Linux repository.
Follow the per-distro instructions at [the linux-repo project](https://github.com/tamnd/linux-repo), then:

```bash
sudo apt install vec   # Debian, Ubuntu
sudo dnf install vec   # Fedora, RHEL
```

## go install

With a Go toolchain:

```bash
go install github.com/tamnd/vec/cmd/vec@latest
```

## Docker

The image bundles the static binary and writes databases under the mounted `/data` volume:

```bash
docker run -v "$PWD/data:/data" ghcr.io/tamnd/vec articles.vec ".tables"
```

## Prebuilt archives

Every release attaches `tar.gz` archives (a `zip` on Windows) for Linux, macOS, Windows, and FreeBSD on amd64 and arm64, plus checksums, a CycloneDX SBOM, and a cosign signature.
Download from [the releases page](https://github.com/tamnd/vec/releases), unpack, and put `vec` on your `PATH`.

## Build from source

```bash
git clone https://github.com/tamnd/vec
cd vec
go build -o vec ./cmd/vec
```

The build is pure Go with cgo off, so it cross-compiles with `GOOS` and `GOARCH` and needs no C toolchain.

## Verify

```bash
vec --version
```

## The library

To embed vec instead of running the binary, add the module to your project:

```bash
go get github.com/tamnd/vec
```

```go
import "github.com/tamnd/vec"
```

The [quick start](/getting-started/quick-start/) shows both the CLI and the library on the same example.
