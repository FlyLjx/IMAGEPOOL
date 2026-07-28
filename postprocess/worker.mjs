import fs from "node:fs/promises";
import readline from "node:readline";

import * as ort from "onnxruntime-node";
import sharp from "sharp";

const SUPER_RESOLUTION_SCALE = 4;
const SUPER_RESOLUTION_TRIGGER_RATIO = 2 / 3;
const RESTORE_MAX_EDGE = 2048;
const RESTORE_TILE = 256;
const RESTORE_PAD = 32;
const MODEL_ALIGNMENT = 64;
const TILE = 256;
const PAD = 16;

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

function parseImageSize(value) {
  const match = /^\s*(\d+)\s*x\s*(\d+)\s*$/i.exec(String(value || ""));
  if (!match) return null;
  const width = Number(match[1]);
  const height = Number(match[2]);
  return width > 0 && height > 0 ? { width, height } : null;
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

async function runSuperResolutionTile(session, source, width, height) {
  const area = width * height;
  const chw = new Float32Array(3 * area);
  for (let index = 0; index < area; index++) {
    chw[index] = (source[index * 3] ?? 0) / 255;
    chw[area + index] = (source[index * 3 + 1] ?? 0) / 255;
    chw[2 * area + index] = (source[index * 3 + 2] ?? 0) / 255;
  }
  const inputName = session.inputNames[0];
  const outputName = session.outputNames[0];
  if (!inputName || !outputName) throw new Error("Real-ESRGAN model input/output is missing");
  const output = await session.run({
    [inputName]: new ort.Tensor("float32", chw, [1, 3, height, width]),
  });
  const tensor = output[outputName];
  if (!tensor) throw new Error("Real-ESRGAN model output is missing");
  const outputHeight = tensor.dims[2] ?? height * SUPER_RESOLUTION_SCALE;
  const outputWidth = tensor.dims[3] ?? width * SUPER_RESOLUTION_SCALE;
  const values = tensor.data;
  const outputArea = outputWidth * outputHeight;
  const buffer = Buffer.allocUnsafe(outputArea * 3);
  for (let index = 0; index < outputArea; index++) {
    buffer[index * 3] = clamp255((values[index] ?? 0) * 255);
    buffer[index * 3 + 1] = clamp255((values[outputArea + index] ?? 0) * 255);
    buffer[index * 3 + 2] = clamp255((values[2 * outputArea + index] ?? 0) * 255);
  }
  return { data: buffer, width: outputWidth, height: outputHeight };
}

async function superResolve(image, modelPath) {
  const session = await getSession(modelPath);
  const metadata = await sharp(image).metadata();
  if (!metadata.width || !metadata.height) throw new Error("image dimensions are missing");
  const width = metadata.width;
  const height = metadata.height;
  const source = await sharp(image).removeAlpha().raw().toBuffer();
  const outputWidth = width * SUPER_RESOLUTION_SCALE;
  const outputHeight = height * SUPER_RESOLUTION_SCALE;
  const output = Buffer.allocUnsafe(outputWidth * outputHeight * 3);

  for (let tileY = 0; tileY < height; tileY += TILE) {
    for (let tileX = 0; tileX < width; tileX += TILE) {
      const validXEnd = Math.min(tileX + TILE, width);
      const validYEnd = Math.min(tileY + TILE, height);
      const paddedXStart = Math.max(0, tileX - PAD);
      const paddedYStart = Math.max(0, tileY - PAD);
      const paddedXEnd = Math.min(width, validXEnd + PAD);
      const paddedYEnd = Math.min(height, validYEnd + PAD);
      const paddedWidth = paddedXEnd - paddedXStart;
      const paddedHeight = paddedYEnd - paddedYStart;
      const tile = Buffer.allocUnsafe(paddedWidth * paddedHeight * 3);
      for (let y = 0; y < paddedHeight; y++) {
        const sourceOffset = ((paddedYStart + y) * width + paddedXStart) * 3;
        source.copy(tile, y * paddedWidth * 3, sourceOffset, sourceOffset + paddedWidth * 3);
      }
      const upscaled = await runSuperResolutionTile(session, tile, paddedWidth, paddedHeight);
      const sourceX = (tileX - paddedXStart) * SUPER_RESOLUTION_SCALE;
      const sourceY = (tileY - paddedYStart) * SUPER_RESOLUTION_SCALE;
      const validWidth = (validXEnd - tileX) * SUPER_RESOLUTION_SCALE;
      const validHeight = (validYEnd - tileY) * SUPER_RESOLUTION_SCALE;
      const destinationX = tileX * SUPER_RESOLUTION_SCALE;
      const destinationY = tileY * SUPER_RESOLUTION_SCALE;
      for (let y = 0; y < validHeight; y++) {
        const sourceOffset = ((sourceY + y) * upscaled.width + sourceX) * 3;
        const destinationOffset = ((destinationY + y) * outputWidth + destinationX) * 3;
        upscaled.data.copy(output, destinationOffset, sourceOffset, sourceOffset + validWidth * 3);
      }
    }
  }
  return sharp(output, { raw: { width: outputWidth, height: outputHeight, channels: 3 } }).png().toBuffer();
}

async function calibrateResolution(image, requestedSize, modelPath, force = false) {
  const target = parseImageSize(requestedSize);
  if (!target) return { buffer: image, applied: false };
  const metadata = await sharp(image).metadata();
  if (!metadata.width || !metadata.height) return { buffer: image, applied: false };
  const actualEdge = Math.max(metadata.width, metadata.height);
  const targetEdge = Math.max(target.width, target.height);
  if (actualEdge >= targetEdge * (force ? 1 : SUPER_RESOLUTION_TRIGGER_RATIO)) {
    return { buffer: image, applied: false };
  }
  const upscaled = await superResolve(image, modelPath);
  const upscaledMetadata = await sharp(upscaled).metadata();
  const upscaledEdge = Math.max(upscaledMetadata.width || 0, upscaledMetadata.height || 0);
  let pipeline = sharp(upscaled);
  if (upscaledEdge > targetEdge) {
    pipeline = pipeline.resize(target.width, target.height, {
      fit: "inside",
      withoutEnlargement: true,
    });
  }
  return { buffer: await pipeline.png().toBuffer(), applied: true };
}

async function processRequest(request) {
  const original = await fs.readFile(request.input_path);
  let current = original;
  let restored = false;
  let superResolved = false;
  if (request.restore) {
    const result = await restoreImage(current, request.restoration_model);
    current = result.buffer;
    restored = result.applied;
  }
  if (request.super_resolution) {
    const result = await calibrateResolution(current, request.requested_size, request.super_model, request.force_super_resolution);
    current = result.buffer;
    superResolved = result.applied;
  }
  await fs.writeFile(request.output_path, current);
  return { ok: true, restored, super_resolved: superResolved, skipped: !restored && !superResolved };
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
