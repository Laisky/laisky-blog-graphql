export interface RuntimeConfig {
  urlPrefix?: string
  publicBasePath?: string
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
