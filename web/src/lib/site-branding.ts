import type { RuntimeSiteConfig } from './runtime-config';

/**
 * applySiteBranding applies site-specific title, favicon, and data attributes.
 * It accepts the runtime site configuration and updates the document accordingly.
 */
export function applySiteBranding(site: RuntimeSiteConfig | undefined): void {
  if (typeof document === 'undefined') {
    return;
  }

  const root = document.documentElement;
  const siteId = site?.id?.trim() ?? '';
  const siteTheme = site?.theme?.trim() ?? siteId;
  const title = site?.title?.trim() ?? '';
  const favicon = site?.favicon?.trim() ?? '';

  if (siteId) {
    root.dataset.siteId = siteId;
  } else {
    delete root.dataset.siteId;
  }

  if (siteTheme) {
    root.dataset.siteTheme = siteTheme;
  } else {
    delete root.dataset.siteTheme;
  }

  if (title) {
    document.title = title;
  }

  if (favicon) {
    setFaviconHref(favicon);
  }
}

/**
 * setFaviconHref updates the favicon <link> tag to the provided URL.
 * It accepts the favicon URL and ensures the tag exists and is updated.
 */
function setFaviconHref(url: string): void {
  const existing = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (existing) {
    existing.href = url;
    return;
  }

  const link = document.createElement('link');
  link.rel = 'icon';
  link.href = url;
  document.head.appendChild(link);
}
