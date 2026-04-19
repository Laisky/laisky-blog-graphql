/**
 * IMAGE_ACCEPTED_MIME_TYPES mirrors the server-side allowlist from
 * imageproc.AllowedInputMIMETypes. Keep in sync with the Go code.
 */
export const IMAGE_ACCEPTED_MIME_TYPES = [
  'image/jpeg',
  'image/png',
  'image/webp',
  'image/bmp',
  'image/tiff',
  'image/gif',
];

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
  const bitmap = await loadBitmap(file);
  const longest = Math.max(bitmap.width, bitmap.height);
  if (longest <= IMAGE_LONGEST_EDGE) {
    return file;
  }
  const scale = IMAGE_LONGEST_EDGE / longest;
  const width = Math.max(1, Math.round(bitmap.width * scale));
  const height = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    return file;
  }
  ctx.drawImage(bitmap, 0, 0, width, height);
  const blob = await canvasToBlob(canvas);
  if (!blob) {
    return file;
  }
  return new File([blob], file.name, { type: 'image/jpeg', lastModified: file.lastModified });
}

function loadBitmap(file: File): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve(img);
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
