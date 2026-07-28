import { afterEach, expect, test } from "bun:test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import sharp from "sharp";

const temporaryDirectories = [];

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map((directory) => fs.rm(directory, { recursive: true, force: true })));
});

function runWorker(request) {
  return new Promise((resolve, reject) => {
    const worker = spawn("node", [path.resolve("worker.mjs")], {
      cwd: path.resolve("."),
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timeout = setTimeout(() => {
      worker.kill();
      reject(new Error(`postprocess worker timed out: ${stderr}`));
    }, 120_000);
    worker.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    worker.stdout.on("data", (chunk) => {
      stdout += String(chunk);
      const newline = stdout.indexOf("\n");
      if (newline < 0) return;
      clearTimeout(timeout);
      worker.kill();
      try {
        resolve(JSON.parse(stdout.slice(0, newline)));
      } catch (error) {
        reject(new Error(`invalid postprocess response: ${stdout}\n${stderr}`, { cause: error }));
      }
    });
    worker.on("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    worker.on("exit", (code) => {
      if (code !== 0 && !stdout.includes("\n")) {
        clearTimeout(timeout);
        reject(new Error(`postprocess worker exited with ${code}: ${stderr}`));
      }
    });
    worker.stdin.write(`${JSON.stringify(request)}\n`);
  });
}

test("SCUNet restores images larger than one tile without changing dimensions", async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "image-pool-worker-test-"));
  temporaryDirectories.push(directory);
  const inputPath = path.join(directory, "input.png");
  const outputPath = path.join(directory, "output.png");
  const width = 640;
  const height = 448;
  const pixels = Buffer.allocUnsafe(width * height * 3);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const offset = (y * width + x) * 3;
      pixels[offset] = x % 256;
      pixels[offset + 1] = y % 256;
      pixels[offset + 2] = (x + y) % 256;
    }
  }
  await sharp(pixels, { raw: { width, height, channels: 3 } }).png().toFile(inputPath);

  const result = await runWorker({
    id: "restore-test",
    input_path: inputPath,
    output_path: outputPath,
    requested_size: `${width}x${height}`,
    restore: true,
    super_resolution: false,
    force_super_resolution: false,
    super_model: path.resolve("models/realesr-general-x4v3.onnx"),
    restoration_model: path.resolve("models/scunet-color-real-gan.onnx"),
  });

  expect(result).toMatchObject({ id: "restore-test", ok: true, restored: true });
  const metadata = await sharp(outputPath).metadata();
  expect(metadata.width).toBe(width);
  expect(metadata.height).toBe(height);
}, 120_000);
