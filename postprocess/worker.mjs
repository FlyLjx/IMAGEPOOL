import fs from "node:fs/promises";
import readline from "node:readline";

import * as ort from "onnxruntime-node";
import sharp from "sharp";

const RESTORE_MAX_EDGE = 2048;
const RESTORE_TILE = 256;
const RESTORE_PAD = 32;
const MODEL_ALIGNMENT = 64;

sharp.concurrency(2);

const sessions = new Map();

async function getSession(modelPath) {
  if (!modelPath) throw new Error("ONNX model path is empty");
  let session = sessions.get(modelPath);
  if (!session) {
    session = ort.InferenceSession.create(modelPath);
    sessions.set(modelPath, session);
  }
  return session;
}

function clamp255(value) {
  if (value <= 0) return 0;
  if (value >= 255) return 255;
  return Math.round(value);
}

async function restoreImage(image, modelPath) {
  const metadata = await sharp(image).metadata();
  const width = metadata.width;
  const height = metadata.height;
  if (!width || !height || Math.max(width, height) > RESTORE_MAX_EDGE) {
    return { buffer: image, applied: false };
  }
  const session = await getSession(modelPath);
  const source = await sharp(image).removeAlpha().raw().toBuffer();
  const restored = Buffer.allocUnsafe(width * height * 3);

  for (let tileY = 0; tileY < height; tileY += RESTORE_TILE) {
    for (let tileX = 0; tileX < width; tileX += RESTORE_TILE) {
      const validXEnd = Math.min(tileX + RESTORE_TILE, width);
      const validYEnd = Math.min(tileY + RESTORE_TILE, height);
      const contextXStart = Math.max(0, tileX - RESTORE_PAD);
      const contextYStart = Math.max(0, tileY - RESTORE_PAD);
      const contextXEnd = Math.min(width, validXEnd + RESTORE_PAD);
      const contextYEnd = Math.min(height, validYEnd + RESTORE_PAD);
      const contextWidth = contextXEnd - contextXStart;
      const contextHeight = contextYEnd - contextYStart;
      const context = Buffer.allocUnsafe(contextWidth * contextHeight * 3);

      for (let y = 0; y < contextHeight; y++) {
        const sourceOffset = ((contextYStart + y) * width + contextXStart) * 3;
        source.copy(context, y * contextWidth * 3, sourceOffset, sourceOffset + contextWidth * 3);
      }

      const paddedWidth = Math.ceil(contextWidth / MODEL_ALIGNMENT) * MODEL_ALIGNMENT;
      const paddedHeight = Math.ceil(contextHeight / MODEL_ALIGNMENT) * MODEL_ALIGNMENT;
      let padded = context;
      if (paddedWidth !== contextWidth || paddedHeight !== contextHeight) {
        padded = await sharp(context, { raw: { width: contextWidth, height: contextHeight, channels: 3 } })
          .extend({
            right: paddedWidth - contextWidth,
            bottom: paddedHeight - contextHeight,
            extendWith: "mirror",
          })
          .raw()
          .toBuffer();
      }

      const result = await runRestorationTile(session, padded, paddedWidth, paddedHeight);
      const resultX = tileX - contextXStart;
      const resultY = tileY - contextYStart;
      const validWidth = validXEnd - tileX;
      const validHeight = validYEnd - tileY;
      if (resultX + validWidth > result.width || resultY + validHeight > result.height) {
        throw new Error("SCUNet model output dimensions are invalid");
      }
      for (let y = 0; y < validHeight; y++) {
        const resultOffset = ((resultY + y) * result.width + resultX) * 3;
        const destinationOffset = ((tileY + y) * width + tileX) * 3;
        result.data.copy(restored, destinationOffset, resultOffset, resultOffset + validWidth * 3);
      }
    }
  }

  return {
    buffer: await sharp(restored, { raw: { width, height, channels: 3 } }).png().toBuffer(),
    applied: true,
  };
}

async function runRestorationTile(session, source, width, height) {
  const inputName = session.inputNames[0];
  const outputName = session.outputNames[0];
  if (!inputName || !outputName) throw new Error("SCUNet model input/output is missing");
  const area = width * height;
  const chw = new Float32Array(3 * area);
  for (let index = 0; index < area; index++) {
    chw[index] = (source[index * 3] ?? 0) / 255;
    chw[area + index] = (source[index * 3 + 1] ?? 0) / 255;
    chw[2 * area + index] = (source[index * 3 + 2] ?? 0) / 255;
  }
  const output = await session.run({
    [inputName]: new ort.Tensor("float32", chw, [1, 3, height, width]),
  });
  const tensor = output[outputName];
  if (!tensor) throw new Error("SCUNet model output is missing");
  const values = tensor.data;
  const outputHeight = tensor.dims[2] ?? height;
  const outputWidth = tensor.dims[3] ?? width;
  const outputArea = outputWidth * outputHeight;
  const restored = Buffer.allocUnsafe(outputArea * 3);
  for (let index = 0; index < outputArea; index++) {
    restored[index * 3] = clamp255((values[index] ?? 0) * 255);
    restored[index * 3 + 1] = clamp255((values[outputArea + index] ?? 0) * 255);
    restored[index * 3 + 2] = clamp255((values[2 * outputArea + index] ?? 0) * 255);
  }
  return { data: restored, width: outputWidth, height: outputHeight };
}

async function processRequest(request) {
  const original = await fs.readFile(request.input_path);
  let current = original;
  let restored = false;
  if (request.restore) {
    const result = await restoreImage(current, request.restoration_model);
    current = result.buffer;
    restored = result.applied;
  }
  await fs.writeFile(request.output_path, current);
  return { ok: true, restored, skipped: !restored };
}

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
let chain = Promise.resolve();

input.on("line", (line) => {
  chain = chain.then(async () => {
    let request;
    try {
      request = JSON.parse(line);
      const result = await processRequest(request);
      process.stdout.write(`${JSON.stringify({ id: request.id, ...result })}\n`);
    } catch (error) {
      process.stdout.write(`${JSON.stringify({
        id: request?.id ?? 0,
        ok: false,
        error: error instanceof Error ? error.message : String(error),
      })}\n`);
    }
  });
});
