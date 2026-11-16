import { afterEach, describe, expect, it } from 'vitest';

import { applySiteBranding } from './site-branding';

describe('applySiteBranding favicon', () => {
  afterEach(() => {
    document.head.innerHTML = '';
  });

  it('drops the static MIME type hint when a configured icon replaces the default', () => {
    document.head.innerHTML = '<link rel="icon" type="image/png" href="https://s3.laisky.com/uploads/2025/12/mcp_icon.png">';

    applySiteBranding({ favicon: 'https://sso.example.test/icon.svg' });

    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    expect(icon?.getAttribute('href')).toBe('https://sso.example.test/icon.svg');
    expect(icon?.hasAttribute('type')).toBe(false);
  });

  it('keeps the default icon and its type when no icon is configured', () => {
    document.head.innerHTML = '<link rel="icon" type="image/png" href="https://s3.laisky.com/uploads/2025/12/mcp_icon.png">';

    applySiteBranding({});

    expect(document.querySelector('link[rel="icon"]')?.getAttribute('type')).toBe('image/png');
  });
});
