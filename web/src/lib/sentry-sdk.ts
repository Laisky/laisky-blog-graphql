// Explicit errors-only exports keep replay, tracing and unrelated SDK helpers out of the runtime chunk.
export { init, captureException, globalHandlersIntegration, dedupeIntegration, makeFetchTransport } from '@sentry/react';
