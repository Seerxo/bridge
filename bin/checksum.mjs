import { createHash } from "node:crypto";

export function verifyChecksum(name, binary, checksums) {
  const expected = checksums.split("\n").find(line => line.endsWith(`  ${name}`))?.split(" ")[0];
  if (!expected) throw new Error(`Checksum missing for ${name}`);
  if (createHash("sha256").update(binary).digest("hex") !== expected) throw new Error("Binary checksum mismatch");
}
