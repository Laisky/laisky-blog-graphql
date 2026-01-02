const BEARER_PREFIX = /^Bearer\s+/i;

export function normalizeApiKey(value: string): string {
    let output = (value ?? '').trim();
    while (output && BEARER_PREFIX.test(output)) {
        output = output.replace(BEARER_PREFIX, '').trim();
    }
    return output;
}

export function buildAuthorizationHeader(apiKey: string): string {
    const token = normalizeApiKey(apiKey);
    return token ? `Bearer ${token}` : '';
}

export function resolveCurrentApiBasePath(): string {
    if (typeof window === 'undefined') {
        return '/';
    }
    const path = window.location.pathname || '/';
    return path.endsWith('/') ? path : `${path}/`;
}
