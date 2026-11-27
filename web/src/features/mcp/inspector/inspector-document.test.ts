import { describe, expect, it } from 'vitest';

import { SRCDOC_HISTORY_GUARD, buildInspectorDocument } from './inspector-document';

const embedderBaseURI = 'https://mcp.example.test/mcp/inspector';

type HistoryMethod = (state: unknown, unused: string, url?: string | URL | null) => void;

interface FakeHistoryInstance {
  replaceState: HistoryMethod;
  pushState: HistoryMethod;
}

interface FakeFrame {
  history: FakeHistoryInstance;
  HistoryClass: { prototype: FakeHistoryInstance };
  href: () => string;
  attempts: string[];
  installGuard: () => void;
}

/**
 * createFrame models the browser rule for an iframe srcdoc document: history URLs resolve against
 * the embedding page's base URL, and only a URL equal to the current document URL apart from its
 * fragment may be written. Anything else throws a SecurityError, which Chrome does even for '#tab'.
 */
function createFrame(options: { href?: string; failure?: () => Error } = {}): FakeFrame {
  let href = options.href ?? 'about:srcdoc';
  const attempts: string[] = [];

  const write = (url?: string | URL | null) => {
    if (url === undefined || url === null) {
      attempts.push('(no url)');
      return;
    }
    attempts.push(String(url));
    if (options.failure) {
      throw options.failure();
    }
    const target = new URL(String(url), embedderBaseURI);
    if (target.href.split('#')[0] !== href.split('#')[0]) {
      throw new DOMException(`Failed to execute history update: '${target.href}' cannot be used in '${href}'.`, 'SecurityError');
    }
    href = target.href;
  };

  class FakeHistory {
    replaceState(_state: unknown, _unused: string, url?: string | URL | null): void {
      write(url);
    }

    pushState(_state: unknown, _unused: string, url?: string | URL | null): void {
      write(url);
    }
  }

  const fakeWindow = {
    History: FakeHistory,
    URL,
    document: { baseURI: embedderBaseURI },
    location: {
      get href() {
        return href;
      },
      get protocol() {
        return new URL(href).protocol;
      },
    },
  };

  return {
    history: new FakeHistory(),
    HistoryClass: FakeHistory,
    href: () => href,
    attempts,
    installGuard: () => {
      new Function('window', SRCDOC_HISTORY_GUARD)(fakeWindow);
    },
  };
}

describe('SRCDOC_HISTORY_GUARD', () => {
  it('lets the Inspector clear its tab hash inside srcdoc instead of throwing', () => {
    // Control: the Inspector's disconnect call fails in srcdoc without the guard.
    const unguarded = createFrame({ href: 'about:srcdoc#tools' });
    expect(() => unguarded.history.replaceState(null, '', 'srcdoc')).toThrow(expect.objectContaining({ name: 'SecurityError' }));

    const frame = createFrame({ href: 'about:srcdoc#tools' });
    frame.installGuard();

    expect(() => frame.history.replaceState(null, '', 'srcdoc')).not.toThrow();
    expect(frame.href()).toBe('about:srcdoc');
    expect(frame.attempts).toEqual(['srcdoc', 'about:srcdoc']);
  });

  it('keeps only the requested fragment for replaceState and pushState', () => {
    const frame = createFrame();
    frame.installGuard();

    frame.history.replaceState(null, '', '#tools');
    expect(frame.href()).toBe('about:srcdoc#tools');

    frame.history.pushState(null, '', '/elsewhere?tab=1#ping');
    expect(frame.href()).toBe('about:srcdoc#ping');
  });

  it('leaves calls that already succeed untouched', () => {
    const frame = createFrame();
    frame.installGuard();

    frame.history.replaceState(null, '', 'about:srcdoc#resources');
    frame.history.replaceState({ step: 1 }, '');

    expect(frame.href()).toBe('about:srcdoc#resources');
    expect(frame.attempts).toEqual(['about:srcdoc#resources', '(no url)']);
  });

  it('rethrows errors that are not URL-rewrite security errors', () => {
    const frame = createFrame({ failure: () => new TypeError('state could not be cloned') });
    frame.installGuard();

    expect(() => frame.history.replaceState(null, '', 'srcdoc')).toThrow(TypeError);
    expect(frame.attempts).toEqual(['srcdoc']);
  });

  it('does not patch history outside an about: document', () => {
    const frame = createFrame({ href: 'https://mcp.example.test/inspector/' });
    const { replaceState, pushState } = frame.HistoryClass.prototype;
    frame.installGuard();

    expect(frame.HistoryClass.prototype.replaceState).toBe(replaceState);
    expect(frame.HistoryClass.prototype.pushState).toBe(pushState);
  });
});

describe('buildInspectorDocument', () => {
  it('installs the history guard before the Inspector bundle loads', () => {
    const html = buildInspectorDocument('/assets/index-abc12345.js', '/assets/index-def67890.css');
    const guardAt = html.indexOf(`<script>${SRCDOC_HISTORY_GUARD}</script>`);
    const styleAt = html.indexOf('<link rel="stylesheet" href="/assets/index-def67890.css">');
    const bundleAt = html.indexOf('<script type="module" src="/assets/index-abc12345.js"></script>');

    expect(guardAt).toBeGreaterThan(-1);
    expect(styleAt).toBeGreaterThan(guardAt);
    expect(bundleAt).toBeGreaterThan(styleAt);
    expect(html.indexOf('<div id="root"')).toBeLessThan(bundleAt);
  });
});
