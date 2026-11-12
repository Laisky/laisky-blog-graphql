import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { IMAGE_LONGEST_EDGE, preshrinkImage, preshrinkImages } from './image-utils';

type FakeBitmap = { width: number; height: number; close: ReturnType<typeof vi.fn> };

function makeBitmap(width: number, height: number): FakeBitmap {
  return { width, height, close: vi.fn() };
}

function pngFile(name = 'photo.png'): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type: 'image/png', lastModified: 1234 });
}

/** FakeImage mimics an HTMLImageElement that loads (or fails) as soon as src is set. */
class FakeImage {
  static nextSize = { width: 100, height: 100 };
  static fail = false;
  width = 0;
  height = 0;
  onload: (() => void) | null = null;
  onerror: ((err: unknown) => void) | null = null;
  decode = vi.fn(() => Promise.resolve());
  set src(_value: string) {
    queueMicrotask(() => {
      if (FakeImage.fail) {
        this.onerror?.(new Error('decode failed'));
        return;
      }
      this.width = FakeImage.nextSize.width;
      this.height = FakeImage.nextSize.height;
      this.onload?.();
    });
  }
}

describe('preshrinkImage', () => {
  let drawImage: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    drawImage = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawImage } as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (callback: BlobCallback) {
      callback(new Blob(['jpeg-bytes'], { type: 'image/jpeg' }));
    });
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, writable: true, value: vi.fn(() => 'blob:test') });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, writable: true, value: vi.fn() });
    FakeImage.fail = false;
    FakeImage.nextSize = { width: 100, height: 100 };
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Reflect.deleteProperty(URL, 'createObjectURL');
    Reflect.deleteProperty(URL, 'revokeObjectURL');
  });

  it('decodes with createImageBitmap and downscales to the longest-edge budget', async () => {
    const bitmap = makeBitmap(IMAGE_LONGEST_EDGE * 2, IMAGE_LONGEST_EDGE * 2 / 3);
    const createImageBitmap = vi.fn().mockResolvedValue(bitmap);
    vi.stubGlobal('createImageBitmap', createImageBitmap);
    vi.stubGlobal('Image', FakeImage);

    const input = pngFile();
    const output = await preshrinkImage(input);

    expect(createImageBitmap).toHaveBeenCalledWith(input, { imageOrientation: 'from-image' });
    expect(drawImage).toHaveBeenCalledWith(bitmap, 0, 0, IMAGE_LONGEST_EDGE, 512);
    expect(bitmap.close).toHaveBeenCalledTimes(1);
    expect(output).not.toBe(input);
    expect(output.name).toBe('photo.png');
    expect(output.type).toBe('image/jpeg');
    expect(output.lastModified).toBe(1234);
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it('returns the original file when it already fits and releases the bitmap', async () => {
    const bitmap = makeBitmap(800, 600);
    vi.stubGlobal('createImageBitmap', vi.fn().mockResolvedValue(bitmap));

    const input = pngFile();
    await expect(preshrinkImage(input)).resolves.toBe(input);
    expect(drawImage).not.toHaveBeenCalled();
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it('falls back to an image element when createImageBitmap is unavailable', async () => {
    vi.stubGlobal('createImageBitmap', undefined);
    vi.stubGlobal('Image', FakeImage);
    FakeImage.nextSize = { width: 600, height: IMAGE_LONGEST_EDGE * 3 };

    const output = await preshrinkImage(pngFile());

    expect(drawImage).toHaveBeenCalledWith(expect.any(FakeImage), 0, 0, 200, IMAGE_LONGEST_EDGE);
    const image = drawImage.mock.calls[0][0] as FakeImage;
    expect(image.decode).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:test');
    expect(output.type).toBe('image/jpeg');
  });

  it('falls back to an image element when createImageBitmap rejects', async () => {
    vi.stubGlobal('createImageBitmap', vi.fn().mockRejectedValue(new Error('unsupported')));
    vi.stubGlobal('Image', FakeImage);
    FakeImage.nextSize = { width: 400, height: 300 };

    const input = pngFile();
    await expect(preshrinkImage(input)).resolves.toBe(input);
    expect(URL.createObjectURL).toHaveBeenCalledWith(input);
  });

  it('still rejects when neither decoder can read the file', async () => {
    vi.stubGlobal('createImageBitmap', vi.fn().mockRejectedValue(new Error('unsupported')));
    vi.stubGlobal('Image', FakeImage);
    FakeImage.fail = true;

    await expect(preshrinkImage(pngFile())).rejects.toBeDefined();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:test');
  });

  it('passes through files outside the accepted MIME list without decoding', async () => {
    const createImageBitmap = vi.fn();
    vi.stubGlobal('createImageBitmap', createImageBitmap);
    const input = new File(['x'], 'notes.txt', { type: 'text/plain' });

    await expect(preshrinkImage(input)).resolves.toBe(input);
    expect(createImageBitmap).not.toHaveBeenCalled();
  });
});

describe('preshrinkImages', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('keeps input order and yields to the main thread between files', async () => {
    const order: string[] = [];
    const yieldFn = vi.fn(async () => {
      order.push('yield');
    });
    vi.stubGlobal('scheduler', { yield: yieldFn });
    vi.stubGlobal(
      'createImageBitmap',
      vi.fn(async (file: File) => {
        order.push(file.name);
        return makeBitmap(10, 10);
      })
    );

    const files = [pngFile('a.png'), pngFile('b.png'), pngFile('c.png')];
    const output = await preshrinkImages(files);

    expect(output).toEqual(files);
    expect(order).toEqual(['a.png', 'yield', 'b.png', 'yield', 'c.png']);
  });

  it('yields with a macrotask when scheduler.yield is unavailable', async () => {
    vi.stubGlobal('scheduler', undefined);
    vi.stubGlobal('createImageBitmap', vi.fn().mockResolvedValue(makeBitmap(10, 10)));
    const setTimeoutSpy = vi.spyOn(globalThis, 'setTimeout');

    await preshrinkImages([pngFile('a.png'), pngFile('b.png')]);

    expect(setTimeoutSpy).toHaveBeenCalledWith(expect.any(Function), 0);
  });
});
