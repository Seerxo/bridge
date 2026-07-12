#!/usr/bin/env node

import { execFileSync, spawn } from "node:child_process";
import { chmod, mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { verifyChecksum } from "./checksum.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const pkg = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
const version = pkg.version;
const model = process.env.BRIDGE_MODEL || "z-ai/glm-5.2";
const modelURL = "https://build.nvidia.com/z-ai/glm-5.2";
const command = process.argv[2] || "claude";

if (command === "--version" || command === "version") {
  console.log(version);
  process.exit(0);
}
if (command !== "claude") {
  console.error("Usage: bridge [claude] [claude options]");
  process.exit(1);
}

const apiKey = process.env.NVIDIA_API_KEY || loadKey() || await onboard();
const binary = process.env.BRIDGE_BINARY || await downloadBinary();
const port = process.env.BRIDGE_PORT || String(18080 + Math.floor(Math.random() * 1000));
const server = spawn(binary, [], {
  env: {
    ...process.env,
    BRIDGE_ADDR: `127.0.0.1:${port}`,
    BRIDGE_UPSTREAM_API_KEY: apiKey,
  },
  stdio: ["ignore", "ignore", "pipe"],
});

let serverError = "";
server.stderr.on("data", chunk => { serverError += chunk; });
const stop = () => server.kill();
process.once("SIGINT", stop);
process.once("SIGTERM", stop);
process.once("exit", stop);

try {
  await waitForHealth(port, server);
  const claude = spawn(process.env.CLAUDE_BIN || "claude", ["--model", model, ...process.argv.slice(3)], {
    env: {
      ...process.env,
      ANTHROPIC_BASE_URL: `http://127.0.0.1:${port}`,
      ANTHROPIC_AUTH_TOKEN: "bridge-local",
    },
    stdio: "inherit",
  });
  const code = await new Promise((resolve, reject) => {
    claude.once("error", reject);
    claude.once("exit", value => resolve(value ?? 1));
  });
  process.exitCode = code;
} catch (error) {
  console.error(error.message);
  if (serverError) console.error(serverError.trim());
  process.exitCode = 1;
} finally {
  stop();
}

function loadKey() {
  try {
    if (process.platform === "darwin") {
      return execFileSync("security", ["find-generic-password", "-a", process.env.USER || "bridge", "-s", "com.seerxo.bridge.nvidia", "-w"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
    }
    if (process.platform === "linux") {
      return execFileSync("secret-tool", ["lookup", "service", "seerxo-bridge", "provider", "nvidia"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
    }
  } catch {}
  return "";
}

async function onboard() {
  if (!process.stdin.isTTY) throw new Error("NVIDIA_API_KEY is required");
  console.error("Opening NVIDIA GLM-5.2 to generate an API key...");
  openURL(modelURL);
  const key = await hiddenPrompt("Paste the API key: ");
  if (!key) throw new Error("API key cannot be empty");
  saveKey(key);
  return key;
}

function saveKey(key) {
  try {
    if (process.platform === "darwin") {
      execFileSync("security", ["add-generic-password", "-U", "-a", process.env.USER || "bridge", "-s", "com.seerxo.bridge.nvidia", "-w", key], { stdio: "ignore" });
      return;
    }
    if (process.platform === "linux") {
      execFileSync("secret-tool", ["store", "--label=Seerxo Bridge NVIDIA API key", "service", "seerxo-bridge", "provider", "nvidia"], { input: key, stdio: ["pipe", "ignore", "ignore"] });
      return;
    }
  } catch {}
  console.error("No system keychain found; the key will be requested next time.");
}

function openURL(url) {
  const spec = process.platform === "darwin"
    ? ["open", [url]]
    : process.platform === "win32"
      ? ["cmd", ["/c", "start", "", url]]
      : ["xdg-open", [url]];
  try {
    spawn(spec[0], spec[1], { detached: true, stdio: "ignore" }).unref();
  } catch {}
}

async function hiddenPrompt(label) {
  process.stderr.write(label);
  process.stdin.setRawMode(true);
  process.stdin.resume();
  let value = "";
  return new Promise(resolve => {
    const onData = chunk => {
      for (const char of chunk.toString()) {
        if (char === "\r" || char === "\n") {
          process.stdin.off("data", onData);
          process.stdin.setRawMode(false);
          process.stdin.pause();
          process.stderr.write("\n");
          resolve(value);
        } else if (char === "\u0003") {
          process.stdin.setRawMode(false);
          process.exit(130);
        } else if (char === "\u007f") {
          value = value.slice(0, -1);
        } else {
          value += char;
        }
      }
    };
    process.stdin.on("data", onData);
  });
}

async function downloadBinary() {
  const arch = process.arch === "x64" ? "x64" : process.arch === "arm64" ? "arm64" : "";
  const platform = ["darwin", "linux", "win32"].includes(process.platform) ? process.platform : "";
  if (!arch || !platform) throw new Error(`Unsupported platform: ${process.platform}-${process.arch}`);

  const extension = platform === "win32" ? ".exe" : "";
  const name = `bridge-${platform}-${arch}${extension}`;
  const target = join(homedir(), ".cache", "seerxo-bridge", `v${version}`, name);
  try {
    await chmod(target, 0o755);
    return target;
  } catch {}

  console.error(`Downloading Seerxo Bridge v${version}...`);
  const releaseURL = `https://github.com/Seerxo/bridge/releases/download/v${version}`;
  const [response, checksums] = await Promise.all([
    fetch(`${releaseURL}/${name}`, { redirect: "follow" }),
    fetch(`${releaseURL}/checksums.txt`, { redirect: "follow" }),
  ]);
  if (!response.ok) throw new Error(`Binary download failed: HTTP ${response.status}`);
  if (!checksums.ok) throw new Error(`Checksum download failed: HTTP ${checksums.status}`);
  const binary = Buffer.from(await response.arrayBuffer());
  verifyChecksum(name, binary, await checksums.text());
  await mkdir(dirname(target), { recursive: true });
  const temporary = join(tmpdir(), `${name}-${process.pid}`);
  await writeFile(temporary, binary, { mode: 0o755 });
  await rename(temporary, target);
  return target;
}

async function waitForHealth(port, child) {
  for (let i = 0; i < 50; i++) {
    if (child.exitCode !== null) throw new Error("Bridge failed to start");
    try {
      const response = await fetch(`http://127.0.0.1:${port}/health`);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error("Bridge startup timed out");
}
