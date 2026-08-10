#!/usr/bin/env node
"use strict";

const crypto = require("crypto");
const fs = require("fs");
const https = require("https");
const os = require("os");
const path = require("path");
const zlib = require("zlib");
const { spawnSync: systemSpawnSync } = require("child_process");
const { version: VERSION } = require("./package.json");

const REPOSITORY = "BRYANN2K/maestro";
const TAG = `v${VERSION}`;
const RELEASE_BASE_URL = `https://github.com/${REPOSITORY}/releases/download/${TAG}`;
const CHECKSUM_ASSET = "checksums.txt";
const METADATA_FILE = "install.json";
const MAX_ARCHIVE_BYTES = 128 * 1024 * 1024;
const MAX_CHECKSUM_BYTES = 1024 * 1024;
const MAX_EXTRACTED_BYTES = 256 * 1024 * 1024;
const MAX_BINARY_BYTES = 128 * 1024 * 1024;
const DOWNLOAD_TIMEOUT_MS = 30_000;
const MAX_REDIRECTS = 5;
const LOCK_TIMEOUT_MS = 120_000;
const LOCK_POLL_MS = 50;
const LOCK_ORPHAN_GRACE_MS = 1_000;
const LOCK_MALFORMED_STALE_MS = 90_000;

const TARGETS = Object.freeze({
  "darwin:x64": Object.freeze({ os: "darwin", arch: "amd64", format: "tar.gz" }),
  "darwin:arm64": Object.freeze({ os: "darwin", arch: "arm64", format: "tar.gz" }),
  "linux:x64": Object.freeze({ os: "linux", arch: "amd64", format: "tar.gz" }),
  "linux:arm64": Object.freeze({ os: "linux", arch: "arm64", format: "tar.gz" }),
  "win32:x64": Object.freeze({ os: "windows", arch: "amd64", format: "zip" }),
  "win32:arm64": Object.freeze({ os: "windows", arch: "arm64", format: "zip" }),
});

function targetFor(platform = process.platform, arch = process.arch) {
  const target = TARGETS[`${platform}:${arch}`];
  if (!target) {
    const supported = "macOS, Linux, and Windows on x64 or arm64";
    throw new Error(`unsupported platform ${platform}/${arch}; supported: ${supported}`);
  }
  return target;
}

function executableName(platform = process.platform) {
  return platform === "win32" ? "maestro.exe" : "maestro";
}

function assetName(platform = process.platform, arch = process.arch) {
  const target = targetFor(platform, arch);
  return `maestro_${VERSION}_${target.os}_${target.arch}.${target.format}`;
}

function releaseURLs(platform = process.platform, arch = process.arch) {
  const asset = assetName(platform, arch);
  return {
    asset,
    archive: `${RELEASE_BASE_URL}/${asset}`,
    checksums: `${RELEASE_BASE_URL}/${CHECKSUM_ASSET}`,
  };
}

function cacheRoot(home = os.homedir()) {
  return path.join(home, ".maestro", "bin");
}

function versionRoot(home = os.homedir()) {
  return path.join(cacheRoot(home), `v${VERSION}`);
}

function versionedBinDir(
  home = os.homedir(),
  platform = process.platform,
  arch = process.arch
) {
  targetFor(platform, arch);
  return path.join(versionRoot(home), `${platform}-${arch}`);
}

function binaryPath(
  home = os.homedir(),
  platform = process.platform,
  arch = process.arch
) {
  return path.join(versionedBinDir(home, platform, arch), executableName(platform));
}

function metadataPath(
  home = os.homedir(),
  platform = process.platform,
  arch = process.arch
) {
  return path.join(versionedBinDir(home, platform, arch), METADATA_FILE);
}

function isWindows(platform) {
  return platform === "win32";
}

function owns(stat) {
  return typeof process.getuid !== "function" || stat.uid === process.getuid();
}

function privateMode(stat, expected, platform) {
  return isWindows(platform) || (stat.mode & 0o777) === expected;
}

function safeDirectory(directory, platform, fsImpl = fs) {
  try {
    const stat = fsImpl.lstatSync(directory);
    return (
      !stat.isSymbolicLink() &&
      stat.isDirectory() &&
      owns(stat) &&
      privateMode(stat, 0o700, platform)
    );
  } catch {
    return false;
  }
}

function ensurePrivateDirectory(directory, platform, fsImpl = fs) {
  try {
    fsImpl.mkdirSync(directory, { mode: 0o700 });
  } catch (error) {
    if (!error || error.code !== "EEXIST") throw error;
  }
  const stat = fsImpl.lstatSync(directory);
  if (stat.isSymbolicLink() || !stat.isDirectory() || !owns(stat)) {
    throw new Error(`unsafe cache directory: ${directory}`);
  }
  if (!isWindows(platform)) fsImpl.chmodSync(directory, 0o700);
}

function ensureCacheParents(home, platform, fsImpl = fs) {
  const maestroDir = path.join(home, ".maestro");
  const binDir = cacheRoot(home);
  const releaseDir = versionRoot(home);
  ensurePrivateDirectory(maestroDir, platform, fsImpl);
  ensurePrivateDirectory(binDir, platform, fsImpl);
  ensurePrivateDirectory(releaseDir, platform, fsImpl);
}

function sha256(data) {
  return crypto.createHash("sha256").update(data).digest("hex");
}

function validSHA256(value) {
  return typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
}

function constantTimeHexEqual(left, right) {
  if (!validSHA256(left) || !validSHA256(right)) return false;
  return crypto.timingSafeEqual(Buffer.from(left, "hex"), Buffer.from(right, "hex"));
}

function readSmallRegularFile(file, maxBytes, mode, platform, fsImpl = fs) {
  const stat = fsImpl.lstatSync(file);
  if (
    stat.isSymbolicLink() ||
    !stat.isFile() ||
    !owns(stat) ||
    !privateMode(stat, mode, platform) ||
    stat.size > maxBytes
  ) {
    throw new Error(`unsafe cache file: ${file}`);
  }
  return fsImpl.readFileSync(file);
}

function expectedMetadata(platform, arch) {
  return {
    schema: 1,
    version: VERSION,
    platform,
    arch,
    asset: assetName(platform, arch),
  };
}

function cacheReady(
  home = os.homedir(),
  platform = process.platform,
  arch = process.arch,
  fsImpl = fs,
  hostPlatform = process.platform
) {
  try {
    const controlledDirectories = [
      path.join(home, ".maestro"),
      cacheRoot(home),
      versionRoot(home),
      versionedBinDir(home, platform, arch),
    ];
    if (!controlledDirectories.every((directory) => safeDirectory(directory, hostPlatform, fsImpl))) {
      return false;
    }

    const bin = binaryPath(home, platform, arch);
    const binaryStat = fsImpl.lstatSync(bin);
    if (
      binaryStat.isSymbolicLink() ||
      !binaryStat.isFile() ||
      !owns(binaryStat) ||
      !privateMode(binaryStat, 0o700, hostPlatform) ||
      binaryStat.size < 1 ||
      binaryStat.size > MAX_BINARY_BYTES
    ) {
      return false;
    }

    const rawMetadata = readSmallRegularFile(
      metadataPath(home, platform, arch),
      8192,
      0o600,
      hostPlatform,
      fsImpl
    );
    const metadata = JSON.parse(rawMetadata.toString("utf8"));
    const expected = expectedMetadata(platform, arch);
    if (
      metadata.schema !== expected.schema ||
      metadata.version !== expected.version ||
      metadata.platform !== expected.platform ||
      metadata.arch !== expected.arch ||
      metadata.asset !== expected.asset ||
      !validSHA256(metadata.archiveSha256) ||
      !validSHA256(metadata.binarySha256)
    ) {
      return false;
    }
    return constantTimeHexEqual(sha256(fsImpl.readFileSync(bin)), metadata.binarySha256);
  } catch {
    return false;
  }
}

function timeoutError(timeoutMs) {
  const error = new Error(`download timed out after ${timeoutMs}ms`);
  error.code = "ETIMEDOUT";
  return error;
}

function downloadBuffer(url, options = {}) {
  const get = options.get || https.get;
  const maxBytes = options.maxBytes || MAX_ARCHIVE_BYTES;
  const timeoutMs = options.timeoutMs || DOWNLOAD_TIMEOUT_MS;
  const maxRedirects = options.maxRedirects ?? MAX_REDIRECTS;
  const deadline = Date.now() + timeoutMs;

  if (!Number.isSafeInteger(maxBytes) || maxBytes < 1) {
    return Promise.reject(new Error("invalid download size limit"));
  }

  return new Promise((resolve, reject) => {
    function request(currentURL, redirectCount) {
      let parsed;
      try {
        parsed = new URL(currentURL);
      } catch {
        reject(new Error(`invalid release URL: ${currentURL}`));
        return;
      }
      if (parsed.protocol !== "https:") {
        reject(new Error(`refusing non-HTTPS release URL: ${parsed.protocol}`));
        return;
      }

      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        reject(timeoutError(timeoutMs));
        return;
      }

      let settled = false;
      let req;
      let activeResponse;
      const finish = (callback, value) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        callback(value);
      };
      const timer = setTimeout(() => {
        const error = timeoutError(timeoutMs);
        if (activeResponse && typeof activeResponse.destroy === "function") {
          activeResponse.destroy(error);
        }
        if (req && typeof req.destroy === "function") req.destroy(error);
        finish(reject, error);
      }, remaining);

      try {
        req = get(
          parsed,
          {
            headers: {
              Accept: "application/octet-stream",
              "User-Agent": `maestro-npm/${VERSION}`,
            },
          },
          (response) => {
            activeResponse = response;
            const discard = () => {
              if (typeof response.destroy === "function") response.destroy();
              else if (typeof response.resume === "function") response.resume();
            };
            const status = response.statusCode || 0;
            const location = response.headers && response.headers.location;
            if ([301, 302, 303, 307, 308].includes(status)) {
              if (!location) {
                discard();
                finish(reject, new Error(`release redirect ${status} had no Location header`));
                return;
              }
              if (redirectCount >= maxRedirects) {
                discard();
                finish(reject, new Error(`release download exceeded ${maxRedirects} redirects`));
                return;
              }
              let next;
              try {
                next = new URL(location, parsed).toString();
                if (new URL(next).protocol !== "https:") {
                  throw new Error("non-HTTPS redirect");
                }
              } catch {
                discard();
                finish(reject, new Error("release redirected to an unsafe URL"));
                return;
              }
              discard();
              finish(() => request(next, redirectCount + 1));
              return;
            }
            if (status !== 200) {
              discard();
              finish(reject, new Error(`release download returned HTTP ${status}`));
              return;
            }

            const rawLength = response.headers && response.headers["content-length"];
            if (rawLength !== undefined) {
              const contentLength = Number(Array.isArray(rawLength) ? rawLength[0] : rawLength);
              if (!Number.isSafeInteger(contentLength) || contentLength < 0) {
                if (typeof response.destroy === "function") response.destroy();
                finish(reject, new Error("release download had an invalid Content-Length"));
                return;
              }
              if (contentLength > maxBytes) {
                if (typeof response.destroy === "function") response.destroy();
                finish(reject, new Error(`release download exceeds ${maxBytes} bytes`));
                return;
              }
            }

            const chunks = [];
            let received = 0;
            response.on("data", (chunk) => {
              if (settled) return;
              const data = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
              received += data.length;
              if (received > maxBytes) {
                if (typeof response.destroy === "function") response.destroy();
                finish(reject, new Error(`release download exceeds ${maxBytes} bytes`));
                return;
              }
              chunks.push(data);
            });
            response.once("aborted", () =>
              finish(reject, new Error("release download was interrupted"))
            );
            response.once("error", (error) => finish(reject, error));
            response.once("end", () => finish(resolve, Buffer.concat(chunks, received)));
          }
        );
        req.once("error", (error) => {
          if (activeResponse && typeof activeResponse.destroy === "function") {
            activeResponse.destroy(error);
          }
          finish(reject, error);
        });
      } catch (error) {
        finish(reject, error);
      }
    }

    request(url, 0);
  });
}

function parseChecksums(manifest, expectedAsset) {
  const text = Buffer.isBuffer(manifest) ? manifest.toString("utf8") : String(manifest);
  if (text.includes("\u0000")) throw new Error("checksum manifest contains NUL bytes");
  let match = null;
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const parsed = /^([A-Fa-f0-9]{64})[ \t]+\*?([^\r\n]+)$/.exec(line);
    if (!parsed) throw new Error("checksum manifest contains an invalid line");
    const filename = parsed[2].trim();
    if (filename === expectedAsset) {
      if (match) throw new Error(`checksum manifest contains duplicate entries for ${expectedAsset}`);
      match = parsed[1].toLowerCase();
    }
  }
  if (!match) throw new Error(`checksum manifest does not contain ${expectedAsset}`);
  return match;
}

function cleanArchivePath(rawName) {
  if (!rawName || rawName.includes("\u0000") || rawName.includes("\\")) {
    throw new Error("archive contains an unsafe path");
  }
  if (rawName.startsWith("/") || /^[A-Za-z]:/.test(rawName)) {
    throw new Error(`archive contains an absolute path: ${rawName}`);
  }
  const withoutTrailingSlash = rawName.replace(/\/+$/, "");
  const parts = withoutTrailingSlash.split("/");
  if (parts.some((part) => part === ".." || part === "")) {
    throw new Error(`archive contains path traversal: ${rawName}`);
  }
  const cleaned = parts.filter((part) => part !== ".").join("/");
  if (!cleaned || cleaned.startsWith("../")) {
    throw new Error(`archive contains an unsafe path: ${rawName}`);
  }
  return cleaned;
}

function decodeArchiveName(buffer) {
  const nul = buffer.indexOf(0);
  const slice = nul === -1 ? buffer : buffer.subarray(0, nul);
  const name = slice.toString("utf8");
  if (name.includes("\ufffd")) throw new Error("archive path is not valid UTF-8");
  return name;
}

function parseTarOctal(field, label) {
  const value = field.toString("ascii").replace(/\0.*$/, "").trim();
  if (!value) return 0;
  if (!/^[0-7]+$/.test(value)) throw new Error(`tar contains an invalid ${label}`);
  const parsed = Number.parseInt(value, 8);
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new Error(`tar contains an invalid ${label}`);
  }
  return parsed;
}

function tarHeaderChecksum(header) {
  let total = 0;
  for (let index = 0; index < header.length; index += 1) {
    total += index >= 148 && index < 156 ? 32 : header[index];
  }
  return total;
}

function extractTarGz(archive, expectedBinary) {
  let tar;
  try {
    tar = zlib.gunzipSync(archive, { maxOutputLength: MAX_EXTRACTED_BYTES });
  } catch (error) {
    throw new Error(`could not decompress release tar.gz: ${error.message}`);
  }

  let offset = 0;
  let binary = null;
  let sawEnd = false;
  const entries = new Set();
  while (offset + 512 <= tar.length) {
    const header = tar.subarray(offset, offset + 512);
    offset += 512;
    if (header.every((byte) => byte === 0)) {
      sawEnd = true;
      break;
    }
    const storedChecksum = parseTarOctal(header.subarray(148, 156), "header checksum");
    if (storedChecksum !== tarHeaderChecksum(header)) {
      throw new Error("tar header checksum mismatch");
    }
    const namePart = decodeArchiveName(header.subarray(0, 100));
    const prefix = decodeArchiveName(header.subarray(345, 500));
    const rawName = prefix ? `${prefix}/${namePart}` : namePart;
    const entryName = cleanArchivePath(rawName);
    if (entries.has(entryName)) throw new Error(`archive contains duplicate path: ${entryName}`);
    entries.add(entryName);

    const size = parseTarOctal(header.subarray(124, 136), "entry size");
    const type = String.fromCharCode(header[156] || 48);
    if (type !== "0" && type !== "5") {
      throw new Error(`archive contains a link or non-regular entry: ${entryName}`);
    }
    if (type === "5" && size !== 0) {
      throw new Error(`archive directory has data: ${entryName}`);
    }
    if (size > MAX_EXTRACTED_BYTES || offset + size > tar.length) {
      throw new Error(`archive entry has an invalid size: ${entryName}`);
    }
    const end = offset + size;
    if (type === "0" && entryName === expectedBinary) {
      if (binary) throw new Error(`archive contains duplicate ${expectedBinary}`);
      if (size < 1 || size > MAX_BINARY_BYTES) {
        throw new Error(`release binary has an invalid size: ${size}`);
      }
      binary = Buffer.from(tar.subarray(offset, end));
    }
    offset += Math.ceil(size / 512) * 512;
    if (offset > tar.length) throw new Error("tar entry exceeds the archive boundary");
  }
  if (!sawEnd) throw new Error("tar archive is truncated");
  if (!binary) throw new Error(`release archive is missing ${expectedBinary}`);
  return binary;
}

const CRC32_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let value = 0; value < 256; value += 1) {
    let crc = value;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc & 1) !== 0 ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1;
    }
    table[value] = crc >>> 0;
  }
  return table;
})();

function crc32(data) {
  let crc = 0xffffffff;
  for (const byte of data) crc = CRC32_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function findZipEnd(archive) {
  const minimum = Math.max(0, archive.length - 65_557);
  for (let offset = archive.length - 22; offset >= minimum; offset -= 1) {
    if (archive.readUInt32LE(offset) !== 0x06054b50) continue;
    const commentLength = archive.readUInt16LE(offset + 20);
    if (offset + 22 + commentLength === archive.length) return offset;
  }
  throw new Error("zip archive has no valid end record");
}

function extractZip(archive, expectedBinary) {
  if (archive.length < 22) throw new Error("zip archive is truncated");
  const endOffset = findZipEnd(archive);
  const disk = archive.readUInt16LE(endOffset + 4);
  const centralDisk = archive.readUInt16LE(endOffset + 6);
  const entriesOnDisk = archive.readUInt16LE(endOffset + 8);
  const totalEntries = archive.readUInt16LE(endOffset + 10);
  const centralSize = archive.readUInt32LE(endOffset + 12);
  const centralOffset = archive.readUInt32LE(endOffset + 16);
  if (disk !== 0 || centralDisk !== 0 || entriesOnDisk !== totalEntries) {
    throw new Error("multi-disk zip archives are not supported");
  }
  if (totalEntries === 0xffff || centralSize === 0xffffffff || centralOffset === 0xffffffff) {
    throw new Error("ZIP64 release archives are not supported");
  }
  if (centralOffset + centralSize !== endOffset) {
    throw new Error("zip central directory is malformed");
  }

  let offset = centralOffset;
  let declaredExtractedBytes = 0;
  let binary = null;
  const names = new Set();
  for (let entryIndex = 0; entryIndex < totalEntries; entryIndex += 1) {
    if (offset + 46 > endOffset || archive.readUInt32LE(offset) !== 0x02014b50) {
      throw new Error("zip central directory entry is malformed");
    }
    const madeBy = archive.readUInt16LE(offset + 4);
    const flags = archive.readUInt16LE(offset + 8);
    const method = archive.readUInt16LE(offset + 10);
    const expectedCRC = archive.readUInt32LE(offset + 16);
    const compressedSize = archive.readUInt32LE(offset + 20);
    const uncompressedSize = archive.readUInt32LE(offset + 24);
    const nameLength = archive.readUInt16LE(offset + 28);
    const extraLength = archive.readUInt16LE(offset + 30);
    const commentLength = archive.readUInt16LE(offset + 32);
    const externalAttributes = archive.readUInt32LE(offset + 38);
    const localOffset = archive.readUInt32LE(offset + 42);
    const entryEnd = offset + 46 + nameLength + extraLength + commentLength;
    if (entryEnd > endOffset) throw new Error("zip central directory is truncated");
    if ((flags & 1) !== 0) throw new Error("encrypted zip entries are not supported");
    if (compressedSize === 0xffffffff || uncompressedSize === 0xffffffff) {
      throw new Error("ZIP64 entries are not supported");
    }
    if (method !== 0 && method !== 8) {
      throw new Error(`zip uses unsupported compression method ${method}`);
    }

    const rawNameBuffer = archive.subarray(offset + 46, offset + 46 + nameLength);
    if (rawNameBuffer.includes(0)) throw new Error("zip path contains a NUL byte");
    if ((flags & 0x800) === 0 && rawNameBuffer.some((byte) => byte > 0x7f)) {
      throw new Error("zip contains a non-UTF-8 path");
    }
    const rawName = decodeArchiveName(rawNameBuffer);
    const entryName = cleanArchivePath(rawName);
    if (names.has(entryName)) throw new Error(`archive contains duplicate path: ${entryName}`);
    names.add(entryName);

    const creatorOS = madeBy >>> 8;
    const unixMode = creatorOS === 3 ? externalAttributes >>> 16 : 0;
    const unixType = unixMode & 0o170000;
    const dosDirectory = (externalAttributes & 0x10) !== 0;
    const isDirectory = rawName.endsWith("/") || dosDirectory || unixType === 0o040000;
    if (unixType !== 0 && unixType !== 0o040000 && unixType !== 0o100000) {
      throw new Error(`archive contains a link or non-regular entry: ${entryName}`);
    }
    if (unixType === 0o100000 && isDirectory) {
      throw new Error(`archive has conflicting file type metadata: ${entryName}`);
    }
    if (isDirectory && (compressedSize !== 0 || uncompressedSize !== 0)) {
      throw new Error(`archive directory has data: ${entryName}`);
    }
    declaredExtractedBytes += uncompressedSize;
    if (!Number.isSafeInteger(declaredExtractedBytes) || declaredExtractedBytes > MAX_EXTRACTED_BYTES) {
      throw new Error(`archive expands beyond ${MAX_EXTRACTED_BYTES} bytes`);
    }

    if (localOffset + 30 > centralOffset || archive.readUInt32LE(localOffset) !== 0x04034b50) {
      throw new Error(`zip local entry is malformed: ${entryName}`);
    }
    const localFlags = archive.readUInt16LE(localOffset + 6);
    const localMethod = archive.readUInt16LE(localOffset + 8);
    const localNameLength = archive.readUInt16LE(localOffset + 26);
    const localExtraLength = archive.readUInt16LE(localOffset + 28);
    const localNameStart = localOffset + 30;
    const dataStart = localNameStart + localNameLength + localExtraLength;
    const dataEnd = dataStart + compressedSize;
    if (
      localFlags !== flags ||
      localMethod !== method ||
      dataEnd > centralOffset ||
      !archive.subarray(localNameStart, localNameStart + localNameLength).equals(rawNameBuffer)
    ) {
      throw new Error(`zip local entry disagrees with its directory: ${entryName}`);
    }

    if (!isDirectory && entryName === expectedBinary) {
      if (binary) throw new Error(`archive contains duplicate ${expectedBinary}`);
      if (uncompressedSize < 1 || uncompressedSize > MAX_BINARY_BYTES) {
        throw new Error(`release binary has an invalid size: ${uncompressedSize}`);
      }
      const compressed = archive.subarray(dataStart, dataEnd);
      try {
        binary = method === 0
          ? Buffer.from(compressed)
          : zlib.inflateRawSync(compressed, { maxOutputLength: MAX_BINARY_BYTES });
      } catch (error) {
        throw new Error(`could not decompress ${expectedBinary}: ${error.message}`);
      }
      if (binary.length !== uncompressedSize || crc32(binary) !== expectedCRC) {
        throw new Error(`zip integrity check failed for ${expectedBinary}`);
      }
    }
    offset = entryEnd;
  }
  if (offset !== endOffset) throw new Error("zip central directory has trailing data");
  if (!binary) throw new Error(`release archive is missing ${expectedBinary}`);
  return binary;
}

function extractBinary(archive, format, expectedBinary) {
  if (format === "tar.gz") return extractTarGz(archive, expectedBinary);
  if (format === "zip") return extractZip(archive, expectedBinary);
  throw new Error(`unsupported release archive format: ${format}`);
}

function writeDurableFile(file, data, mode, fsImpl = fs) {
  const descriptor = fsImpl.openSync(file, "wx", mode);
  try {
    fsImpl.writeFileSync(descriptor, data);
    fsImpl.fsyncSync(descriptor);
  } finally {
    fsImpl.closeSync(descriptor);
  }
  fsImpl.chmodSync(file, mode);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function processIsAlive(pid) {
  if (!Number.isSafeInteger(pid) || pid < 1) return false;
  if (pid === process.pid) return true;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return Boolean(error && error.code !== "ESRCH");
  }
}

function readLockOwner(lockDir, platform, fsImpl = fs) {
  try {
    const raw = readSmallRegularFile(
      path.join(lockDir, "owner.json"),
      2048,
      0o600,
      platform,
      fsImpl
    );
    const owner = JSON.parse(raw.toString("utf8"));
    if (
      !Number.isSafeInteger(owner.pid) ||
      owner.pid < 1 ||
      typeof owner.token !== "string" ||
      !/^[a-f0-9]{32}$/.test(owner.token) ||
      typeof owner.startedAt !== "string"
    ) {
      return null;
    }
    return owner;
  } catch {
    return null;
  }
}

function lockOwned(lock, platform, fsImpl = fs) {
  if (!lock) return false;
  const owner = readLockOwner(lock.directory, platform, fsImpl);
  return Boolean(owner && owner.token === lock.token && owner.pid === process.pid);
}

function releaseInstallLock(lock, platform, fsImpl = fs) {
  if (!lockOwned(lock, platform, fsImpl)) return;
  fsImpl.rmSync(lock.directory, { recursive: true, force: true });
}

async function acquireInstallLock(home, platform, arch, options) {
  const fsImpl = options.fs || fs;
  const hostPlatform = options.hostPlatform || process.platform;
  const wait = options.wait || delay;
  const now = options.now || Date.now;
  const isProcessAlive = options.isProcessAlive || processIsAlive;
  const timeoutMs = options.lockTimeoutMs ?? LOCK_TIMEOUT_MS;
  const pollMs = options.lockPollMs ?? LOCK_POLL_MS;
  const orphanGraceMs = options.lockOrphanGraceMs ?? LOCK_ORPHAN_GRACE_MS;
  const malformedStaleMs = options.lockMalformedStaleMs ?? LOCK_MALFORMED_STALE_MS;
  const lockDir = path.join(versionRoot(home), `${platform}-${arch}.install.lock`);
  const token = crypto.randomBytes(16).toString("hex");
  const deadline = now() + timeoutMs;
  for (;;) {
    let created = false;
    try {
      fsImpl.mkdirSync(lockDir, { mode: 0o700 });
      created = true;
      if (!isWindows(hostPlatform)) fsImpl.chmodSync(lockDir, 0o700);
      writeDurableFile(
        path.join(lockDir, "owner.json"),
        `${JSON.stringify({ pid: process.pid, token, startedAt: new Date().toISOString() })}\n`,
        0o600,
        fsImpl
      );
      return { directory: lockDir, token };
    } catch (error) {
      if (created) {
        const owner = readLockOwner(lockDir, hostPlatform, fsImpl);
        if (owner && owner.token === token && owner.pid === process.pid) {
          fsImpl.rmSync(lockDir, { recursive: true, force: true });
        }
        throw error;
      }
      if (!error || error.code !== "EEXIST") throw error;
      if (cacheReady(home, platform, arch, fsImpl, hostPlatform)) return null;

      let lockStat;
      try {
        lockStat = fsImpl.lstatSync(lockDir);
      } catch (statError) {
        if (statError && statError.code === "ENOENT") continue;
        throw statError;
      }
      if (
        lockStat.isSymbolicLink() ||
        !lockStat.isDirectory() ||
        !owns(lockStat) ||
        !privateMode(lockStat, 0o700, hostPlatform)
      ) {
        throw new Error(`unsafe install lock: ${lockDir}`);
      }
      const age = Math.max(0, now() - lockStat.mtimeMs);
      const owner = readLockOwner(lockDir, hostPlatform, fsImpl);
      const deadOwner = owner && age >= orphanGraceMs && !isProcessAlive(owner.pid);
      const abandonedMalformedLock = !owner && age >= malformedStaleMs;
      if (deadOwner || abandonedMalformedLock) {
        const quarantine = `${lockDir}.stale-${process.pid}-${crypto.randomBytes(8).toString("hex")}`;
        try {
          fsImpl.renameSync(lockDir, quarantine);
        } catch (renameError) {
          if (renameError && renameError.code === "ENOENT") continue;
          throw renameError;
        }
        fsImpl.rmSync(quarantine, { recursive: true, force: true });
        continue;
      }

      if (now() >= deadline) {
        throw new Error(`timed out waiting for another Maestro installer after ${timeoutMs}ms`);
      }
      await wait(Math.min(pollMs, Math.max(1, deadline - now())));
    }
  }
}

async function install(options = {}) {
  const home = options.home || os.homedir();
  const platform = options.platform || process.platform;
  const hostPlatform = options.hostPlatform || process.platform;
  const arch = options.arch || process.arch;
  const fsImpl = options.fs || fs;
  const log = options.log || console.error;
  const target = targetFor(platform, arch);
  const urls = releaseURLs(platform, arch);
  const download = options.download || ((url, limits) =>
    downloadBuffer(url, {
      get: options.httpsGet,
      maxBytes: limits.maxBytes,
      timeoutMs: options.downloadTimeoutMs ?? DOWNLOAD_TIMEOUT_MS,
      maxRedirects: options.maxRedirects ?? MAX_REDIRECTS,
    }));

  ensureCacheParents(home, hostPlatform, fsImpl);
  if (cacheReady(home, platform, arch, fsImpl, hostPlatform)) {
    return binaryPath(home, platform, arch);
  }

  const installID = `${process.pid}-${crypto.randomBytes(8).toString("hex")}`;
  const temporaryDir = path.join(versionRoot(home), `.${platform}-${arch}.install-${installID}`);
  const destination = versionedBinDir(home, platform, arch);
  const lock = await acquireInstallLock(home, platform, arch, options);
  if (lock === null) return binaryPath(home, platform, arch);

  let published = false;
  try {
    if (cacheReady(home, platform, arch, fsImpl, hostPlatform)) {
      return binaryPath(home, platform, arch);
    }
    log(`maestro: installing ${TAG} for ${platform}/${arch}...`);

    const checksumManifest = await download(urls.checksums, { maxBytes: MAX_CHECKSUM_BYTES });
    const expectedArchiveHash = parseChecksums(checksumManifest, urls.asset);
    const archive = await download(urls.archive, { maxBytes: MAX_ARCHIVE_BYTES });
    const archiveHash = sha256(archive);
    if (!constantTimeHexEqual(archiveHash, expectedArchiveHash)) {
      throw new Error(`SHA-256 mismatch for ${urls.asset}`);
    }

    const binary = extractBinary(archive, target.format, executableName(platform));
    const binaryHash = sha256(binary);
    fsImpl.mkdirSync(temporaryDir, { mode: 0o700 });
    if (!isWindows(hostPlatform)) fsImpl.chmodSync(temporaryDir, 0o700);
    writeDurableFile(
      path.join(temporaryDir, executableName(platform)),
      binary,
      0o700,
      fsImpl
    );
    const metadata = {
      ...expectedMetadata(platform, arch),
      archiveSha256: archiveHash,
      binarySha256: binaryHash,
    };
    writeDurableFile(
      path.join(temporaryDir, METADATA_FILE),
      `${JSON.stringify(metadata, null, 2)}\n`,
      0o600,
      fsImpl
    );

    if (!lockOwned(lock, hostPlatform, fsImpl)) {
      throw new Error("lost the Maestro install lock before publishing the binary");
    }
    if (fsImpl.existsSync(destination)) {
      fsImpl.rmSync(destination, { recursive: true, force: true });
    }
    fsImpl.renameSync(temporaryDir, destination);
    published = true;
    if (!cacheReady(home, platform, arch, fsImpl, hostPlatform)) {
      throw new Error("installed Maestro binary failed its cache integrity check");
    }
    return binaryPath(home, platform, arch);
  } catch (error) {
    if (published && fsImpl.existsSync(destination)) {
      fsImpl.rmSync(destination, { recursive: true, force: true });
    }
    throw error;
  } finally {
    if (fsImpl.existsSync(temporaryDir)) {
      fsImpl.rmSync(temporaryDir, { recursive: true, force: true });
    }
    releaseInstallLock(lock, hostPlatform, fsImpl);
  }
}

async function run(args = process.argv.slice(2), options = {}) {
  const home = options.home || os.homedir();
  const platform = options.platform || process.platform;
  const hostPlatform = options.hostPlatform || process.platform;
  const arch = options.arch || process.arch;
  const spawnSync = options.spawnSync || systemSpawnSync;
  const fsImpl = options.fs || fs;
  const env = options.env || process.env;
  const log = options.log || console.error;

  try {
    targetFor(platform, arch);
    let bin = binaryPath(home, platform, arch);
    if (!cacheReady(home, platform, arch, fsImpl, hostPlatform)) {
      bin = await install({ ...options, home, platform, arch, fs: fsImpl, log });
    }
    const result = spawnSync(bin, args, { stdio: "inherit", env });
    if (result.error) throw new Error(`could not launch ${bin}: ${result.error.message}`);
    if (result.signal) {
      const signalNumber = os.constants.signals[result.signal];
      const fallbackStatus = Number.isInteger(signalNumber) ? 128 + signalNumber : 1;
      const relaySignal = options.relaySignal || ((signal) => process.kill(process.pid, signal));
      relaySignal(result.signal);
      return fallbackStatus;
    }
    return Number.isInteger(result.status) ? result.status : 1;
  } catch (error) {
    log(`maestro: ${error.message}`);
    return 1;
  }
}

if (require.main === module) {
  run().then(
    (status) => {
      process.exitCode = status;
    },
    (error) => {
      console.error(`maestro: ${error.message}`);
      process.exitCode = 1;
    }
  );
}

module.exports = {
  CHECKSUM_ASSET,
  MAX_ARCHIVE_BYTES,
  MAX_BINARY_BYTES,
  MAX_CHECKSUM_BYTES,
  MAX_EXTRACTED_BYTES,
  RELEASE_BASE_URL,
  TAG,
  TARGETS,
  VERSION,
  assetName,
  binaryPath,
  cacheReady,
  cacheRoot,
  cleanArchivePath,
  crc32,
  downloadBuffer,
  executableName,
  extractBinary,
  install,
  metadataPath,
  parseChecksums,
  releaseURLs,
  run,
  sha256,
  targetFor,
  versionRoot,
  versionedBinDir,
};
