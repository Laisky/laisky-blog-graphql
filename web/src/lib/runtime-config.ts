export interface ToolsConfig {
  web_search: boolean
  web_fetch: boolean
  ask_user: boolean
  get_user_request: boolean
  extract_key_info: boolean
}

export interface RuntimeConfig {
  urlPrefix?: string
  publicBasePath?: string
  tools?: ToolsConfig
}

// Default tools config with all tools enabled
export const defaultToolsConfig: ToolsConfig = {
  web_search: true,
  web_fetch: true,
  ask_user: true,
  get_user_request: true,
  extract_key_info: true,
}

export async function loadRuntimeConfig(): Promise<RuntimeConfig | null> {
  try {
    const response = await fetch('/runtime-config.json', { cache: 'no-store' })
    if (!response.ok) {
      return null
    }

    const data = (await response.json()) as unknown
    if (typeof data === 'object' && data !== null) {
      return data as RuntimeConfig
    }

    return null
  } catch (error) {
    if (import.meta.env.DEV) {
      console.warn('Failed to load runtime config', error)
    }
    return null
  }
}
