"use strict";

const assert = require("node:assert/strict");
const { EventEmitter } = require("events");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { PassThrough } = require("stream");
const { test } = require("node:test");
const zlib = require("zlib");

const launcher = require("./npx-launcher.js");

function tempHome(t) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "maestro-npx-"));
  t.after(() => fs.rmSync(home, { recursive: true, force: true }));
  return home;
}

function writeTarString(header, offset, length, value) {
  Buffer.from(value).copy(header, offset, 0, length);
}

function writeTarOctal(header, offset, length, value) {
  const octal = value.toString(8).padStart(length - 1, "0") + "\0";
  writeTarString(header, offset, length, octal);
}

function makeTarGz(entries) {
  const blocks = [];
  for (const entry of entries) {
    const data = Buffer.from(entry.data || "");
    const header = Buffer.alloc(512);
    writeTarString(header, 0, 100, entry.name);
    writeTarOctal(header, 100, 8, entry.mode || 0o644);
    writeTarOctal(header, 108, 8, 0);
    writeTarOctal(header, 116, 8, 0);
    writeTarOctal(header, 124, 12, data.length);
    writeTarOctal(header, 136, 12, 0);
    header.fill(32, 148, 156);
    header[156] = (entry.type || "0").charCodeAt(0);
    writeTarString(header, 257, 6, "ustar\0");
    writeTarString(header, 263, 2, "00");
    let checksum = 0;
    for (const byte of header) checksum += byte;
    const encodedChecksum = `${checksum.toString(8).padStart(6, "0")}\0 `;
    writeTarString(header, 148, 8, encodedChecksum);
    blocks.push(header, data);
    if (data.length % 512 !== 0) blocks.push(Buffer.alloc(512 - (data.length % 512)));
  }
  blocks.push(Buffer.alloc(1024));
  return zlib.gzipSync(Buffer.concat(blocks));
}

function makeZip(entries) {
  const localParts = [];
  const centralParts = [];
  let localOffset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, "utf8");
    const data = Buffer.from(entry.data || "");
    const method = entry.method ?? 8;
    const compressed = method === 0 ? data : zlib.deflateRawSync(data);
    const checksum = launcher.crc32(data);
    const flags = 0x800;
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(flags, 6);
    local.writeUInt16LE(method, 8);
    local.writeUInt32LE(checksum, 14);
    local.writeUInt32LE(compressed.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    localParts.push(local, name, compressed);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE((3 << 8) | 20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(flags, 8);
    central.writeUInt16LE(method, 10);
    central.writeUInt32LE(checksum, 16);
    central.writeUInt32LE(compressed.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    const mode = entry.mode ?? (entry.name.endsWith("/") ? 0o040755 : 0o100755);
    central.writeUInt32LE(
      ((((mode << 16) >>> 0) | (entry.name.endsWith("/") ? 0x10 : 0)) >>> 0),
      38
    );
    central.writeUInt32LE(localOffset, 42);
    centralParts.push(central, name);
    localOffset += local.length + name.length + compressed.length;
  }
  const central = Buffer.concat(centralParts);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(central.length, 12);
  end.writeUInt32LE(localOffset, 16);
  return Buffer.concat([...localParts, central, end]);
}

function fixtureArchive(platform, binary = Buffer.from("fixture maestro binary")) {
  if (platform === "win32") {
    return makeZip([
      { name: "LICENSE", data: "MIT", mode: 0o100644 },
      { name: "maestro.exe", data: binary, mode: 0o100755 },
    ]);
  }
  return makeTarGz([
    { name: "LICENSE", data: "MIT" },
    { name: "maestro", data: binary, mode: 0o755 },
  ]);
}

function fixtureDownload(platform, arch, options = {}) {
  const archive = options.archive || fixtureArchive(platform, options.binary);
  const asset = launcher.assetName(platform, arch);
  const checksum = options.checksum || launcher.sha256(archive);
  const calls = [];
  async function download(url, limits) {
    calls.push({ url, limits });
    if (options.delay) await new Promise((resolve) => setTimeout(resolve, options.delay));
    if (url.endsWith("/checksums.txt")) {
      return Buffer.from(`${checksum}  ${asset}\n`);
    }
    assert.ok(url.endsWith(`/${asset}`));
    return archive;
  }
  return { archive, calls, download };
}

function fakeHTTPS(routes) {
  return function get(url, _options, callback) {
    const request = new EventEmitter();
    request.destroy = (error) => queueMicrotask(() => request.emit("error", error));
    queueMicrotask(() => {
      const route = routes.get(url.toString());
      if (!route || route.hang) return;
      const response = new PassThrough();
      response.statusCode = route.status ?? 200;
      response.headers = { ...(route.headers || {}) };
      route.response = response;
      callback(response);
      if (route.neverEnd) return;
      if (route.chunks) {
        for (const chunk of route.chunks) response.write(chunk);
        response.end();
      } else {
        response.end(route.body || Buffer.alloc(0));
      }
    });
    return request;
  };
}

test("maps all six supported Node targets to GoReleaser v1 assets", () => {
  const cases = [
    ["darwin", "x64", "maestro_1.0.0_darwin_amd64.tar.gz"],
    ["darwin", "arm64", "maestro_1.0.0_darwin_arm64.tar.gz"],
    ["linux", "x64", "maestro_1.0.0_linux_amd64.tar.gz"],
    ["linux", "arm64", "maestro_1.0.0_linux_arm64.tar.gz"],
    ["win32", "x64", "maestro_1.0.0_windows_amd64.zip"],
    ["win32", "arm64", "maestro_1.0.0_windows_arm64.zip"],
  ];
  for (const [platform, arch, expected] of cases) {
    assert.equal(launcher.assetName(platform, arch), expected);
    assert.equal(launcher.releaseURLs(platform, arch).archive,
      `https://github.com/BRYANN2K/maestro/releases/download/v1.0.0/${expected}`);
  }
  assert.throws(() => launcher.targetFor("freebsd", "x64"), /unsupported platform freebsd\/x64/);
  assert.throws(() => launcher.targetFor("linux", "ia32"), /unsupported platform linux\/ia32/);
});

test("uses an architecture-specific versioned cache and Windows suffix", () => {
  const home = path.join("tmp", "home");
  assert.equal(
    launcher.versionedBinDir(home, "linux", "arm64"),
    path.join(home, ".maestro", "bin", "v1.0.0", "linux-arm64")
  );
  assert.equal(launcher.executableName("linux"), "maestro");
  assert.equal(launcher.executableName("win32"), "maestro.exe");
  assert.match(launcher.binaryPath(home, "win32", "x64"), /win32-x64[/\\]maestro\.exe$/);
});

test("parses an exact checksum entry and rejects malformed or duplicate manifests", () => {
  const asset = "maestro_1.0.0_linux_amd64.tar.gz";
  const hash = "a".repeat(64);
  assert.equal(launcher.parseChecksums(`${"b".repeat(64)}  other\n${hash} *${asset}\n`, asset), hash);
  assert.throws(() => launcher.parseChecksums("not a manifest\n", asset), /invalid line/);
  assert.throws(() => launcher.parseChecksums(`${hash}  other\n`, asset), /does not contain/);
  assert.throws(() => launcher.parseChecksums(`${hash}  ${asset}\n${hash}  ${asset}\n`, asset), /duplicate/);
});

test("follows bounded HTTPS redirects without touching the network", async () => {
  const start = "https://example.test/start";
  const finish = "https://cdn.example.test/asset";
  const redirect = { status: 302, headers: { location: finish }, neverEnd: true };
  const get = fakeHTTPS(new Map([
    [start, redirect],
    [finish, { body: "release" }],
  ]));
  assert.equal((await launcher.downloadBuffer(start, { get, timeoutMs: 1000 })).toString(), "release");
  assert.equal(redirect.response.destroyed, true);

  const loop = fakeHTTPS(new Map([[start, { status: 302, headers: { location: start } }]]));
  await assert.rejects(
    launcher.downloadBuffer(start, { get: loop, timeoutMs: 1000, maxRedirects: 1 }),
    /exceeded 1 redirects/
  );
  const downgrade = fakeHTTPS(new Map([[start, { status: 302, headers: { location: "http://example.test/x" } }]]));
  await assert.rejects(launcher.downloadBuffer(start, { get: downgrade }), /unsafe URL/);

  const denied = { status: 403, neverEnd: true };
  await assert.rejects(
    launcher.downloadBuffer(start, { get: fakeHTTPS(new Map([[start, denied]])), timeoutMs: 20 }),
    /HTTP 403/
  );
  assert.equal(denied.response.destroyed, true);
});

test("enforces download time and size limits", async () => {
  const url = "https://example.test/asset";
  await assert.rejects(
    launcher.downloadBuffer(url, {
      get: fakeHTTPS(new Map([[url, { hang: true }]])),
      timeoutMs: 10,
    }),
    /timed out/
  );
  const endlessResponse = { neverEnd: true };
  await assert.rejects(
    launcher.downloadBuffer(url, {
      get: fakeHTTPS(new Map([[url, endlessResponse]])),
      timeoutMs: 10,
    }),
    /timed out/
  );
  assert.equal(endlessResponse.response.destroyed, true);
  await assert.rejects(
    launcher.downloadBuffer(url, {
      get: fakeHTTPS(new Map([[url, { headers: { "content-length": "11" }, body: "too large!!" }]])),
      maxBytes: 10,
    }),
    /exceeds 10 bytes/
  );
  await assert.rejects(
    launcher.downloadBuffer(url, {
      get: fakeHTTPS(new Map([[url, { chunks: ["123456", "789012"] }]])),
      maxBytes: 10,
    }),
    /exceeds 10 bytes/
  );
});

test("extracts regular root binaries from tar.gz and zip", () => {
  assert.equal(
    launcher.extractBinary(fixtureArchive("linux"), "tar.gz", "maestro").toString(),
    "fixture maestro binary"
  );
  assert.equal(
    launcher.extractBinary(fixtureArchive("win32"), "zip", "maestro.exe").toString(),
    "fixture maestro binary"
  );
});

test("rejects traversal, links, non-regular entries, and missing binaries in tar", () => {
  assert.throws(
    () => launcher.extractBinary(makeTarGz([{ name: "../maestro", data: "x" }]), "tar.gz", "maestro"),
    /path traversal/
  );
  assert.throws(
    () => launcher.extractBinary(makeTarGz([{ name: "link", type: "2" }, { name: "maestro", data: "x" }]), "tar.gz", "maestro"),
    /link or non-regular/
  );
  assert.throws(
    () => launcher.extractBinary(makeTarGz([{ name: "LICENSE", data: "x" }]), "tar.gz", "maestro"),
    /missing maestro/
  );
});

test("rejects traversal, symlinks, and missing binaries in zip listings", () => {
  assert.throws(
    () => launcher.extractBinary(makeZip([{ name: "../maestro.exe", data: "x" }]), "zip", "maestro.exe"),
    /path traversal/
  );
  assert.throws(
    () => launcher.extractBinary(makeZip([
      { name: "link", data: "maestro.exe", mode: 0o120777 },
      { name: "maestro.exe", data: "x" },
    ]), "zip", "maestro.exe"),
    /link or non-regular/
  );
  assert.throws(
    () => launcher.extractBinary(makeZip([{ name: "LICENSE", data: "x" }]), "zip", "maestro.exe"),
    /missing maestro\.exe/
  );
  assert.throws(
    () => launcher.extractBinary(makeZip([{ name: "maestro.exe\u0000hidden", data: "x" }]), "zip", "maestro.exe"),
    /NUL byte/
  );
  assert.throws(
    () => launcher.extractBinary(makeZip([
      { name: "pipe/", data: "", method: 0, mode: 0o010644 },
      { name: "maestro.exe", data: "x" },
    ]), "zip", "maestro.exe"),
    /link or non-regular/
  );
});

test("rejects an archive when its SHA-256 does not match", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64", { checksum: "0".repeat(64) });
  await assert.rejects(
    launcher.install({ home, platform: "linux", arch: "x64", download: fixture.download, log() {} }),
    /SHA-256 mismatch/
  );
  assert.equal(launcher.cacheReady(home, "linux", "x64"), false);
});

test("installs a verified tar binary with private integrity metadata", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64");
  const bin = await launcher.install({
    home,
    platform: "linux",
    arch: "x64",
    download: fixture.download,
    log() {},
  });
  assert.equal(bin, launcher.binaryPath(home, "linux", "x64"));
  assert.equal(fs.readFileSync(bin, "utf8"), "fixture maestro binary");
  if (process.platform !== "win32") {
    assert.equal(fs.lstatSync(bin).mode & 0o777, 0o700);
    assert.equal(fs.lstatSync(launcher.metadataPath(home, "linux", "x64")).mode & 0o777, 0o600);
  }
  assert.equal(launcher.cacheReady(home, "linux", "x64"), true);
  assert.equal(fixture.calls.length, 2);
});

test("installs maestro.exe from the Windows zip", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("win32", "arm64");
  const bin = await launcher.install({
    home,
    platform: "win32",
    arch: "arm64",
    download: fixture.download,
    log() {},
  });
  assert.match(bin, /maestro\.exe$/);
  assert.equal(fs.readFileSync(bin, "utf8"), "fixture maestro binary");
  assert.equal(launcher.cacheReady(home, "win32", "arm64"), true);
});

test("uses host filesystem rules independently from the release target", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64");
  const bin = await launcher.install({
    home,
    platform: "linux",
    arch: "x64",
    hostPlatform: "win32",
    download: fixture.download,
    log() {},
  });
  assert.equal(fs.readFileSync(bin, "utf8"), "fixture maestro binary");
  assert.equal(launcher.cacheReady(home, "linux", "x64", fs, "win32"), true);
});

test("keeps POSIX cache checks for a Windows release installed on a POSIX host", async (t) => {
  if (process.platform === "win32") {
    return t.skip("POSIX permission bits are unavailable on Windows");
  }
  const home = tempHome(t);
  const fixture = fixtureDownload("win32", "x64");
  const bin = await launcher.install({
    home,
    platform: "win32",
    arch: "x64",
    hostPlatform: "linux",
    download: fixture.download,
    log() {},
  });
  fs.chmodSync(bin, 0o777);
  assert.equal(launcher.cacheReady(home, "win32", "x64", fs, "linux"), false);
});

test("never accepts a symlinked cached binary", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64");
  const bin = await launcher.install({
    home,
    platform: "linux",
    arch: "x64",
    download: fixture.download,
    log() {},
  });
  const target = path.join(home, "attacker-binary");
  fs.writeFileSync(target, "attacker");
  fs.unlinkSync(bin);
  fs.symlinkSync(target, bin);
  assert.equal(launcher.cacheReady(home, "linux", "x64"), false);
});

test("refuses a symlinked cache directory without writing through it", async (t) => {
  const home = tempHome(t);
  const outside = tempHome(t);
  fs.symlinkSync(outside, path.join(home, ".maestro"));
  const fixture = fixtureDownload("linux", "x64");
  await assert.rejects(
    launcher.install({
      home,
      platform: "linux",
      arch: "x64",
      download: fixture.download,
      log() {},
    }),
    /unsafe cache directory/
  );
  assert.deepEqual(fs.readdirSync(outside), []);
  assert.equal(fixture.calls.length, 0);
});

test("does not execute a replaced cached binary", async (t) => {
  const home = tempHome(t);
  const first = fixtureDownload("linux", "x64", { binary: Buffer.from("trusted one") });
  await launcher.install({ home, platform: "linux", arch: "x64", download: first.download, log() {} });
  const bin = launcher.binaryPath(home, "linux", "x64");
  fs.writeFileSync(bin, "replaced");
  fs.chmodSync(bin, 0o700);
  assert.equal(launcher.cacheReady(home, "linux", "x64"), false);

  const second = fixtureDownload("linux", "x64", { binary: Buffer.from("trusted two") });
  const launched = [];
  const status = await launcher.run(["version"], {
    home,
    platform: "linux",
    arch: "x64",
    download: second.download,
    log() {},
    spawnSync(command) {
      launched.push({ command, content: fs.readFileSync(command, "utf8") });
      return { status: 0 };
    },
  });
  assert.equal(status, 0);
  assert.deepEqual(launched, [{ command: bin, content: "trusted two" }]);
});

test("serializes concurrent installs and downloads once", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64", { delay: 15 });
  const options = {
    home,
    platform: "linux",
    arch: "x64",
    download: fixture.download,
    lockPollMs: 1,
    log() {},
  };
  const [first, second] = await Promise.all([
    launcher.install(options),
    launcher.install(options),
  ]);
  assert.equal(first, second);
  assert.equal(fixture.calls.length, 2);
  assert.equal(launcher.cacheReady(home, "linux", "x64"), true);
});

test("quarantines a dead install owner's stale lock and recovers", async (t) => {
  const home = tempHome(t);
  const controlledDirectories = [
    path.join(home, ".maestro"),
    launcher.cacheRoot(home),
    launcher.versionRoot(home),
  ];
  for (const directory of controlledDirectories) {
    fs.mkdirSync(directory, { mode: 0o700 });
    fs.chmodSync(directory, 0o700);
  }
  const lockDir = path.join(launcher.versionRoot(home), "linux-x64.install.lock");
  fs.mkdirSync(lockDir, { mode: 0o700 });
  fs.chmodSync(lockDir, 0o700);
  fs.writeFileSync(
    path.join(lockDir, "owner.json"),
    `${JSON.stringify({
      pid: 999_999,
      token: "a".repeat(32),
      startedAt: "2000-01-01T00:00:00.000Z",
    })}\n`,
    { mode: 0o600 }
  );
  fs.chmodSync(path.join(lockDir, "owner.json"), 0o600);

  const fixture = fixtureDownload("linux", "x64");
  const bin = await launcher.install({
    home,
    platform: "linux",
    arch: "x64",
    download: fixture.download,
    isProcessAlive() { return false; },
    lockOrphanGraceMs: 0,
    lockPollMs: 1,
    log() {},
  });
  assert.equal(bin, launcher.binaryPath(home, "linux", "x64"));
  assert.equal(fixture.calls.length, 2);
  assert.equal(fs.existsSync(lockDir), false);
  assert.deepEqual(
    fs.readdirSync(launcher.versionRoot(home)).filter((name) => name.includes("stale-")),
    []
  );
});

test("cleans lock and temporary state after a failed atomic install", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload("linux", "x64", { checksum: "f".repeat(64) });
  await assert.rejects(
    launcher.install({ home, platform: "linux", arch: "x64", download: fixture.download, log() {} }),
    /SHA-256 mismatch/
  );
  const releaseDir = launcher.versionRoot(home);
  const leftovers = fs.readdirSync(releaseDir).filter((name) => name.includes("install"));
  assert.deepEqual(leftovers, []);
  assert.equal(fs.existsSync(launcher.versionedBinDir(home, "linux", "x64")), false);
});

test("forwards exact arguments, stdio, environment, and exit status", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload(process.platform, process.arch);
  await launcher.install({ home, download: fixture.download, log() {} });
  const args = ["--dir", "a path", "$(touch nope)", "--", "\"quoted\""];
  const env = { PATH: "/test/path", MAESTRO_TEST: "yes" };
  const calls = [];
  const status = await launcher.run(args, {
    home,
    env,
    spawnSync(command, actualArgs, options) {
      calls.push({ command, actualArgs, options });
      return { status: 17 };
    },
  });
  assert.equal(status, 17);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].command, launcher.binaryPath(home));
  assert.deepEqual(calls[0].actualArgs, args);
  assert.deepEqual(calls[0].options, { stdio: "inherit", env });
});

test("reports unsupported targets without downloading or spawning", async (t) => {
  const home = tempHome(t);
  let touched = false;
  const logs = [];
  const status = await launcher.run([], {
    home,
    platform: "aix",
    arch: "ppc64",
    download() { touched = true; },
    spawnSync() { touched = true; },
    log(message) { logs.push(message); },
  });
  assert.equal(status, 1);
  assert.equal(touched, false);
  assert.match(logs.join("\n"), /unsupported platform aix\/ppc64/);
});

test("returns one when launching the binary itself fails", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload(process.platform, process.arch);
  await launcher.install({ home, download: fixture.download, log() {} });
  const logs = [];
  const status = await launcher.run([], {
    home,
    spawnSync() { return { error: new Error("spawn denied") }; },
    log(message) { logs.push(message); },
  });
  assert.equal(status, 1);
  assert.match(logs.join("\n"), /spawn denied/);
});

test("relays child termination signals and preserves their shell exit code", async (t) => {
  const home = tempHome(t);
  const fixture = fixtureDownload(process.platform, process.arch);
  await launcher.install({ home, download: fixture.download, log() {} });
  const relayed = [];
  const status = await launcher.run([], {
    home,
    spawnSync() { return { status: null, signal: "SIGTERM" }; },
    relaySignal(signal) { relayed.push(signal); },
  });
  assert.deepEqual(relayed, ["SIGTERM"]);
  assert.equal(status, 128 + os.constants.signals.SIGTERM);
});

test("the launcher has no Go, shell, tar, or PowerShell install dependency", () => {
  const source = fs.readFileSync(path.join(__dirname, "npx-launcher.js"), "utf8");
  assert.doesNotMatch(source, /spawn(?:Sync)?\s*\(\s*["'](?:go|tar|powershell|pwsh|sh|bash|cmd)/i);
  assert.doesNotMatch(source, /const\s*{\s*exec(?:File)?(?:Sync)?\b/);
  assert.doesNotMatch(source, /child_process[^\n]*\.exec(?:File)?(?:Sync)?\b/);
});
