/**
 * IMAGE_ACCEPTED_MIME_TYPES mirrors the server-side allowlist from
 * imageproc.AllowedInputMIMETypes. Keep in sync with the Go code.
 */
export const IMAGE_ACCEPTED_MIME_TYPES = ['image/jpeg', 'image/png', 'image/webp', 'image/bmp', 'image/tiff', 'image/gif'];

/** IMAGE_MAX_BYTES is the hard pre-upload cap (20 MiB). */
export const IMAGE_MAX_BYTES = 20 * 1024 * 1024;

/** IMAGE_LONGEST_EDGE is the canonical resize target. */
export const IMAGE_LONGEST_EDGE = 1536;

/**
 * isAcceptedImage returns true when the file's MIME type matches the
 * server-side allowlist.
 */
export function isAcceptedImage(file: File): boolean {
  return IMAGE_ACCEPTED_MIME_TYPES.includes(file.type);
}

/**
 * preshrinkImage runs a canvas-based downscale on oversized inputs so the
 * upload payload stays small. Files that already fit the longest-edge budget
 * are returned unchanged.
 */
export async function preshrinkImage(file: File): Promise<File> {
  if (!isAcceptedImage(file)) {
    return file;
  }
  const image = await decodeImage(file);
  let canvas: HTMLCanvasElement;
  try {
    const longest = Math.max(image.width, image.height);
    if (longest <= IMAGE_LONGEST_EDGE) {
      return file;
    }
    const scale = IMAGE_LONGEST_EDGE / longest;
    const width = Math.max(1, Math.round(image.width * scale));
    const height = Math.max(1, Math.round(image.height * scale));
    canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext('2d');
    if (!ctx) {
      return file;
    }
    ctx.drawImage(image.source, 0, 0, width, height);
  } finally {
    image.release();
  }
  const blob = await canvasToBlob(canvas);
  if (!blob) {
    return file;
  }
  return new File([blob], file.name, { type: 'image/jpeg', lastModified: file.lastModified });
}

/**
 * preshrinkImages downscales files one at a time, yielding to the main thread
 * between files so a batch of large images does not block input and rendering.
 * The output keeps the input order.
 */
export async function preshrinkImages(files: File[]): Promise<File[]> {
  const output: File[] = [];
  for (const [index, file] of files.entries()) {
    if (index > 0) {
      await yieldToMain();
    }
    output.push(await preshrinkImage(file));
  }
  return output;
}

type SchedulerWithYield = { yield?: () => Promise<void> };

/** yieldToMain lets pending input and rendering run before long work continues. */
function yieldToMain(): Promise<void> {
  const scheduler = (globalThis as typeof globalThis & { scheduler?: SchedulerWithYield }).scheduler;
  if (typeof scheduler?.yield === 'function') {
    return scheduler.yield();
  }
  return new Promise((resolve) => setTimeout(resolve, 0));
}

interface DecodedImage {
  source: CanvasImageSource;
  width: number;
  height: number;
  release: () => void;
}

/**
 * decodeImage prefers createImageBitmap, which decodes off the main thread and
 * applies EXIF orientation like an <img> does. It falls back to an image element
 * when the API is missing or cannot decode the format.
 */
async function decodeImage(file: File): Promise<DecodedImage> {
  if (typeof createImageBitmap === 'function') {
    try {
      const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' });
      return { source: bitmap, width: bitmap.width, height: bitmap.height, release: () => bitmap.close() };
    } catch {
      // Some engines decode fewer formats through createImageBitmap than through <img>.
    }
  }
  const img = await loadImageElement(file);
  return { source: img, width: img.width, height: img.height, release: () => undefined };
}

function loadImageElement(file: File): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      // decode() finishes decoding before drawImage, so the draw call does not
      // decode synchronously on the main thread.
      const decoded = typeof img.decode === 'function' ? img.decode().catch(() => undefined) : Promise.resolve();
      void decoded.then(() => {
        URL.revokeObjectURL(url);
        resolve(img);
      });
    };
    img.onerror = (err) => {
      URL.revokeObjectURL(url);
      reject(err);
    };
    img.src = url;
  });
}

function canvasToBlob(canvas: HTMLCanvasElement): Promise<Blob | null> {
  return new Promise((resolve) => {
    canvas.toBlob((blob) => resolve(blob), 'image/jpeg', 0.85);
  });
}
