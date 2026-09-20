# @bryann2k/maestro

**CODE IN CONCERT.**

Version-pinned npm launcher for
[Maestro](https://github.com/BRYANN2K/maestro), the spec-driven AI development
environment for the terminal.

## Run

```sh
npx @bryann2k/maestro
```

Arguments are forwarded unchanged:

```sh
npx @bryann2k/maestro --dir ./my-project
npx @bryann2k/maestro version
npx @bryann2k/maestro spec list
```

Use `npx @bryann2k/maestro@1.1.0` to pin the launcher explicitly.

The launcher requires Node.js 18 or newer. A Go toolchain is **not** required.
It downloads the matching Maestro 1.1.0 archive from GitHub Releases, verifies
the archive against the release SHA-256 checksum manifest, and caches that exact
binary under `~/.maestro/bin/v1.1.0/<platform>-<architecture>/`. The cached
binary is checked against private integrity metadata before every launch.

Complete harness bundles target macOS and Linux on x64/arm64, and Windows x64.
Windows ARM64 is not currently packaged. Python 3.11+ (`python3`) is required
for Stipulate and RLM. The launcher verifies the frontend, runtime and native
library before launch. Windows uses
`maestro.exe` and a ZIP archive; macOS and Linux use TAR.GZ archives. Downloads
use HTTPS with bounded redirects, size, and time. Installation is atomic, so
concurrent `npx` calls cannot observe a partial binary.

The package never resolves a floating Maestro version and does not collect
telemetry. Updating the npm package selects a separate versioned cache entry.

## Other installation paths

Prebuilt archives and `checksums.txt` are available from the
[Maestro 1.1.0 release](https://github.com/BRYANN2K/maestro/releases/tag/v1.1.0).
Developers need Go 1.26.5+, Bun 1.3.14 and Python 3.11+. From the source checkout:

```sh
make build
./bin/maestro
```

Keep the three executables and the native library in `bin/` together.

## License

[MIT](./LICENSE). Third-party notices and license texts are included in the npm
package.

Maestro is free and open source. If it helps you,
[follow **@bryann2k_dev** on X](https://x.com/bryann2k_dev). That's all I ask in
return.
