import { browserReporter, initializeBrowserSentry } from './lib/browser-sentry';

// Initialize optional reporting before application modules execute. SDK failure still starts the app.
initializeBrowserSentry().then(async () => {
  const { bootstrap } = await import('./bootstrap');
  await bootstrap();
}).catch((error) => {
  browserReporter.capture(error, 'bootstrap');
  if (import.meta.env.DEV) console.error('Failed to initialize application', error);
});
