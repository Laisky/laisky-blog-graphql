/**
 * SRCDOC_HISTORY_GUARD runs inside the Inspector's srcdoc iframe before the Inspector bundle.
 *
 * An about:srcdoc document may only rewrite the fragment of its own URL. On disconnect (and so on
 * every Reconnect) the Inspector clears its tab hash with
 * `history.replaceState(null, '', location.pathname + location.search)`. Inside srcdoc that is the
 * relative URL "srcdoc", which resolves against the embedding page's base URL, so the browser throws
 * a SecurityError from a React effect and the whole Inspector unmounts to a blank frame.
 *
 * The guard keeps the native call whenever it succeeds. Only when it throws a SecurityError inside an
 * about: document does it retry on the current document URL with just the requested fragment, which
 * is the only part such a document can change. Other errors still propagate.
 */
export const SRCDOC_HISTORY_GUARD = `(function (window) {
  if (window.location.protocol !== 'about:' || !window.History) {
    return;
  }
  var proto = window.History.prototype;
  ['replaceState', 'pushState'].forEach(function (method) {
    var original = proto[method];
    if (typeof original !== 'function') {
      return;
    }
    proto[method] = function (state, unused, url) {
      try {
        return arguments.length > 2 ? original.call(this, state, unused, url) : original.call(this, state, unused);
      } catch (error) {
        if (url === undefined || url === null || !error || error.name !== 'SecurityError') {
          throw error;
        }
        var hash = '';
        try {
          hash = new window.URL(String(url), window.document.baseURI).hash;
        } catch (parseError) {
          hash = '';
        }
        return original.call(this, state, unused, window.location.href.split('#')[0] + hash);
      }
    };
  });
})(window);`;

/**
 * buildInspectorDocument returns the srcdoc HTML that boots the precompiled Inspector client from
 * the given script and stylesheet URLs, with SRCDOC_HISTORY_GUARD installed before the bundle runs.
 */
export function buildInspectorDocument(scriptUrl: string, styleUrl: string): string {
  return [
    '<!doctype html>',
    '<html lang="en">',
    '  <head>',
    '    <meta charset="UTF-8" />',
    '    <meta name="viewport" content="width=device-width, initial-scale=1.0" />',
    '    <title>MCP Inspector</title>',
    '    <style>',
    '      html, body, #root {',
    '        height: 100%;',
    '        min-height: 100%;',
    '        margin: 0;',
    '      }',
    '      body {',
    '        overflow: auto;',
    '      }',
    '    </style>',
    `    <script>${SRCDOC_HISTORY_GUARD}</script>`,
    `    <link rel="stylesheet" href="${styleUrl}">`,
    '  </head>',
    '  <body>',
    '    <div id="root" class="h-full w-full"></div>',
    `    <script type="module" src="${scriptUrl}"></script>`,
    '  </body>',
    '</html>',
  ].join('\n');
}
