import assert from "node:assert/strict";
import { test } from "node:test";
import { verifyChecksum } from "./checksum.mjs";

test("accepts only the matching release binary", () => {
  const binary = Buffer.from("bridge");
  const checksums = "17f29b073143d8cd97b5bbe492bdeffec1c5fee55cc1fe2112c8b9335f8b6121  bridge-linux-x64\n";
  assert.doesNotThrow(() => verifyChecksum("bridge-linux-x64", binary, checksums));
  assert.throws(() => verifyChecksum("bridge-linux-x64", Buffer.from("tampered"), checksums), /mismatch/);
});
