import { configure } from '@testing-library/react';

// findBy*/waitFor give up after 1 s by default. Under a loaded runner (parallel test
// workers, a busy CI host) a lazy route or a StrictMode double render can take longer
// than that, so a correct test fails at random. 4 s keeps a margin while staying inside
// Vitest's 5 s default test timeout; a passing query still returns as soon as it matches.
configure({ asyncUtilTimeout: 4000 });
