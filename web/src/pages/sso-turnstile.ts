const TURNSTILE_SCRIPT_ID = 'cloudflare-turnstile-script';
const TURNSTILE_SCRIPT_SRC = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';

// TurnstileRenderOptions describes the options accepted by the browser Turnstile API.
export interface TurnstileRenderOptions {
  sitekey: string;
  callback: (token: string) => void;
  'expired-callback'?: () => void;
  'error-callback'?: () => void;
  'timeout-callback'?: () => void;
}

// TurnstileAPI describes the browser Turnstile object used by the SSO login form.
export interface TurnstileAPI {
  render: (container: HTMLElement, options: TurnstileRenderOptions) => string;
  reset: (widgetId?: string) => void;
  remove: (widgetId: string) => void;
}

declare global {
  // Window includes the optional Turnstile API object injected by Cloudflare's script.
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

// getTurnstileAPI returns the browser Turnstile API when the challenge script is ready.
// It accepts no parameters and returns undefined before the script loads.
export function getTurnstileAPI(): TurnstileAPI | undefined {
  return window.turnstile;
}

// loadTurnstileScript loads the Cloudflare Turnstile script once and resolves with its browser API.
// It accepts the document that should receive the script element and returns a promise for the API.
export function loadTurnstileScript(documentRef: Document): Promise<TurnstileAPI> {
  const existing = getTurnstileAPI();
  if (existing) {
    return Promise.resolve(existing);
  }

  return new Promise<TurnstileAPI>((resolve, reject) => {
    let script: HTMLScriptElement | null = null;

    const cleanup = (tid: number | undefined) => {
      if (tid !== undefined) {
        window.clearTimeout(tid);
      }
      if (script) {
        script.removeEventListener('load', resolveIfReady);
        script.removeEventListener('error', rejectWithError);
      }
    };

    const resolveIfReady = () => {
      const api = getTurnstileAPI();
      if (!api) {
        cleanup(timeoutID);
        reject(new Error('Turnstile script loaded but API is unavailable.'));
        return;
      }
      cleanup(timeoutID);
      resolve(api);
    };

    const rejectWithError = () => {
      cleanup(timeoutID);
      reject(new Error('Failed to load Cloudflare Turnstile script.'));
    };

    const timeoutID = window.setTimeout(() => {
      rejectWithError();
    }, 10_000);

    const existingScript = documentRef.getElementById(TURNSTILE_SCRIPT_ID);
    if (existingScript instanceof HTMLScriptElement) {
      script = existingScript;
      script.addEventListener('load', resolveIfReady);
      script.addEventListener('error', rejectWithError);
      const api = getTurnstileAPI();
      if (api) {
        resolveIfReady();
      }
      return;
    }

    script = documentRef.createElement('script');
    script.id = TURNSTILE_SCRIPT_ID;
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.addEventListener('load', resolveIfReady);
    script.addEventListener('error', rejectWithError);
    documentRef.head.appendChild(script);
  });
}
