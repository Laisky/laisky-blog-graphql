export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

export function parseRedirectTarget(rawValue: string | null, origin: string): RedirectTarget {
  const raw = (rawValue ?? '').trim();
  if (!raw) {
    return {
      url: null,
      display: 'Not provided',
      error: 'Missing redirect_to parameter. Provide a redirect target to continue.',
    };
  }

  const baseOrigin = origin && origin !== 'null' ? origin : 'http://localhost';

  let parsed: URL;
  try {
    parsed = new URL(raw, baseOrigin);
  } catch {
    return {
      url: null,
      display: raw,
      error: 'Invalid redirect_to URL. Please provide a valid URL or path.',
    };
  }

  if (!isAllowedRedirectProtocol(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect protocol. Only http and https are allowed.',
    };
  }

  if (!isAllowedRedirectHost(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect host. Only *.laisky.com domains or internal IPs are allowed.',
    };
  }

  return {
    url: parsed,
    display: parsed.toString(),
  };
}

export function isAllowedRedirectProtocol(target: URL): boolean {
  const protocol = target.protocol.toLowerCase();
  return protocol === 'http:' || protocol === 'https:';
}

export function isAllowedRedirectHost(target: URL): boolean {
  const hostname = normalizeHostname(target.hostname);
  return isAllowedLaiskyDomain(hostname) || isInternalIPAddress(hostname);
}

export function normalizeHostname(hostname: string): string {
  const trimmed = hostname.trim().toLowerCase();
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

export function isAllowedLaiskyDomain(hostname: string): boolean {
  return hostname === 'laisky.com' || hostname.endsWith('.laisky.com');
}

export function isInternalIPAddress(hostname: string): boolean {
  const ipv4 = parseIPv4Address(hostname);
  if (ipv4) {
    return isInternalIPv4Address(ipv4);
  }

  if (hostname.includes(':')) {
    return isInternalIPv6Address(hostname);
  }

  return false;
}

export function parseIPv4Address(hostname: string): [number, number, number, number] | null {
  const parts = hostname.split('.');
  if (parts.length !== 4) {
    return null;
  }

  const octets: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) {
      return null;
    }
    const value = Number(part);
    if (Number.isNaN(value) || value < 0 || value > 255) {
      return null;
    }
    octets.push(value);
  }

  return [octets[0], octets[1], octets[2], octets[3]];
}

export function isInternalIPv4Address(octets: [number, number, number, number]): boolean {
  const [first, second] = octets;
  if (first === 10) {
    return true;
  }
  if (first === 172 && second >= 16 && second <= 31) {
    return true;
  }
  if (first === 192 && second === 168) {
    return true;
  }
  if (first === 127) {
    return true;
  }
  if (first === 100 && second >= 64 && second <= 127) {
    return true;
  }
  return false;
}

export function isInternalIPv6Address(hostname: string): boolean {
  let normalized = hostname.toLowerCase();

  if (normalized.startsWith('[') && normalized.endsWith(']')) {
    normalized = normalized.slice(1, -1);
  }

  if (normalized === '::1') {
    return true;
  }

  if (normalized.includes('.')) {
    const ipv4Part = normalized.slice(normalized.lastIndexOf(':') + 1);
    const ipv4 = parseIPv4Address(ipv4Part);
    if (ipv4) {
      return isInternalIPv4Address(ipv4);
    }
  }

  const firstHextet = normalized.split(':').find((part) => part.length > 0);
  if (!firstHextet) {
    return false;
  }

  const value = Number.parseInt(firstHextet, 16);
  if (Number.isNaN(value)) {
    return false;
  }

  return (value & 0xfe00) === 0xfc00;
}

export function buildRedirectUrlWithToken(target: URL, token: string): string {
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
}
