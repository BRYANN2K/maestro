# @bryann2k/maestro

**CODE IN CONCERT.**

Version-pinned npm launcher for
[Maestro](https://github.com/BRYANN2K/maestro), the spec-driven AI development
environment for the terminal.

## Run

```sh
npx @bryann2k/maestro@1.0.0
```

Arguments are forwarded unchanged:

```sh
npx @bryann2k/maestro@1.0.0 --dir ./my-project
npx @bryann2k/maestro@1.0.0 version
npx @bryann2k/maestro@1.0.0 spec list
```

The launcher requires Node.js 18 or newer. A Go toolchain is **not** required.
It downloads the matching Maestro 1.0.0 archive from GitHub Releases, verifies
the archive against the release SHA-256 checksum manifest, and caches that exact
binary under `~/.maestro/bin/v1.0.0/<platform>-<architecture>/`. The cached
binary is checked against private integrity metadata before every launch.

Supported targets are macOS, Linux, and Windows on x64 or arm64. Windows uses
`maestro.exe` and a ZIP archive; macOS and Linux use TAR.GZ archives. Downloads
use HTTPS with bounded redirects, size, and time. Installation is atomic, so
concurrent `npx` calls cannot observe a partial binary.

The package never resolves a floating Maestro version and does not collect
telemetry. Updating the npm package selects a separate versioned cache entry.

## Other installation paths

Prebuilt archives and `checksums.txt` are available from the
[Maestro 1.0.0 release](https://github.com/BRYANN2K/maestro/releases/tag/v1.0.0).
Developers with Go 1.26.5 or newer can build the tagged source directly:

```sh
go install github.com/bryann2k/maestro/cmd/maestro@v1.0.0
```

## License

[MIT](./LICENSE). Third-party notices and license texts are included in the npm
package.
