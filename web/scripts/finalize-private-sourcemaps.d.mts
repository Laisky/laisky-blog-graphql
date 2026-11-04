/** PrivateSourceMapOptions selects build paths and permits deterministic copy failure fixtures. */
export interface PrivateSourceMapOptions {
  enabled?: boolean;
  distDir?: string;
  checkoutDir?: string;
  scratchRoot?: string;
  copyFileFn?: (source: string, destination: string) => Promise<unknown>;
}

/** hasSourceMapDirective recognizes actual source-map directive comments. */
export function hasSourceMapDirective(source: string): boolean;

/** finalizePrivateSourceMaps retains Debug ID pairs privately and removes collected public maps. */
export function finalizePrivateSourceMaps(options?: PrivateSourceMapOptions): Promise<string | undefined>;
