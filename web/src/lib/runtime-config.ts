/**
 * ToolsConfig describes which MCP tools are available for the current site.
 */
export interface ToolsConfig {
  web_search: boolean;
  web_fetch: boolean;
  ask_user: boolean;
  get_user_request: boolean;
  extract_key_info: boolean;
  file_io: boolean;
  memory: boolean;
}

/**
 * RuntimeSiteConfig describes site-specific branding and routing metadata.
 */
export interface RuntimeSiteConfig {
  id?: string;
  title?: string;
  favicon?: string;
  theme?: string;
  router?: string;
  publicBasePath?: string;
  turnstileSiteKey?: string;
}

/**
 * SsoJwtConfig describes the public SSO JWT verification metadata.
 */
export interface SsoJwtConfig {
  algorithm: string;
  type: string;
  issuer: string;
  ttl_seconds: number;
  public_key_pem: string;
  claims_schema: Record<string, unknown>;
}

/**
 * RuntimeConfig describes the runtime configuration fetched from the backend.
 */
export interface RuntimeConfig {
  urlPrefix?: string;
  publicBasePath?: string;
  tools?: ToolsConfig;
  site?: RuntimeSiteConfig;
  // githubOAuthEnabled reports whether the backend has GitHub OAuth credentials
  // configured. The SSO login page hides the GitHub sign-in option when this is
  // false so users never trigger a "github oauth client is not configured" error.
  githubOAuthEnabled?: boolean;
  ssoJwt?: SsoJwtConfig | null;
}

// Default tools config with all tools enabled
export const defaultToolsConfig: ToolsConfig = {
  web_search: true,
  web_fetch: true,
  ask_user: true,
  get_user_request: true,
  extract_key_info: true,
  file_io: true,
  memory: false,
};

/**
 * loadRuntimeConfig fetches the runtime configuration from the backend.
 */
export async function loadRuntimeConfig(): Promise<RuntimeConfig | null> {
  try {
    const response = await fetch('/runtime-config.json', { cache: 'no-store' });
    if (!response.ok) {
      return null;
    }

    const data = (await response.json()) as unknown;
    if (typeof data === 'object' && data !== null) {
      return data as RuntimeConfig;
    }

    return null;
  } catch (error) {
    if (import.meta.env.DEV) {
      console.warn('Failed to load runtime config', error);
    }
    return null;
  }
}
