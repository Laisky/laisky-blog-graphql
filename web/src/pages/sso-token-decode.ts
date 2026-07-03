export interface DecodedJwtToken {
  header: unknown;
  payload: unknown;
}

// decodeJwtPart decodes a base64url JWT part into JSON.
// It accepts one JWT segment and returns the parsed JSON value.
function decodeJwtPart(part: string): unknown {
  const normalized = part.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), '=');
  const binary = window.atob(padded);
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
  const json = new TextDecoder().decode(bytes);
  return JSON.parse(json) as unknown;
}

// decodeJwtToken decodes a compact JWT header and payload without verifying the signature.
// It accepts a compact JWT string and returns parsed header and payload JSON values.
export function decodeJwtToken(token: string): DecodedJwtToken {
  const parts = token.trim().split('.');
  if (parts.length !== 3 || !parts[0] || !parts[1]) {
    throw new Error('Token is not a compact JWT.');
  }

  return {
    header: decodeJwtPart(parts[0]),
    payload: decodeJwtPart(parts[1]),
  };
}

// formatDecodedJwtToken formats a compact JWT as readable decoded JSON.
// It accepts a compact JWT string and returns pretty-printed header and payload JSON.
export function formatDecodedJwtToken(token: string): string {
  const decoded = decodeJwtToken(token);
  return JSON.stringify(decoded, null, 2);
}
