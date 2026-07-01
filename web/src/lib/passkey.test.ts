import { describe, expect, it } from 'vitest';

import { arrayBufferToBase64URL, base64URLToArrayBuffer, decodePasskeyRequestOptions } from './passkey';

describe('passkey base64url helpers', () => {
  it('round trips binary data', () => {
    const input = new Uint8Array([0, 1, 2, 250, 251, 252]).buffer;
    const encoded = arrayBufferToBase64URL(input);
    const decoded = new Uint8Array(base64URLToArrayBuffer(encoded));
    expect(Array.from(decoded)).toEqual([0, 1, 2, 250, 251, 252]);
  });
});

describe('decodePasskeyRequestOptions', () => {
  it('decodes challenge and allowed credential IDs', () => {
    const options = decodePasskeyRequestOptions(
      JSON.stringify({
        publicKey: {
          challenge: arrayBufferToBase64URL(new Uint8Array([1, 2, 3]).buffer),
          allowCredentials: [
            {
              type: 'public-key',
              id: arrayBufferToBase64URL(new Uint8Array([4, 5, 6]).buffer),
            },
          ],
        },
      })
    );

    expect(Array.from(new Uint8Array(options.publicKey?.challenge as ArrayBuffer))).toEqual([1, 2, 3]);
    expect(Array.from(new Uint8Array(options.publicKey?.allowCredentials?.[0].id as ArrayBuffer))).toEqual([4, 5, 6]);
  });
});
