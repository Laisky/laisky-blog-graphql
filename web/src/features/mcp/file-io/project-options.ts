import { callFileAPI } from './client';

/** ProjectPage is a bounded page from the authenticated project discovery API. */
export type ProjectPage = { projects: string[]; has_more: boolean; next_cursor?: string };
export const PROJECT_PAGE_SIZE = 50;
const projectID = /^[A-Za-z0-9_.-]{1,128}$/;

/** isProjectQuery accepts an empty search or a bounded fragment of a project ID. */
export function isProjectQuery(query: string): boolean {
  return query === '' || projectID.test(query);
}

/** parseProjectPage rejects malformed data rather than displaying an ambiguous page. */
export function parseProjectPage(value: unknown, after = ''): ProjectPage {
  if (!value || typeof value !== 'object') throw new Error('Invalid project discovery response.');
  const page = value as Record<string, unknown>;
  if (!Array.isArray(page.projects) || page.projects.length > PROJECT_PAGE_SIZE
    || !page.projects.every((item): item is string => typeof item === 'string' && projectID.test(item))
    || new Set(page.projects).size !== page.projects.length || typeof page.has_more !== 'boolean') {
    throw new Error('Invalid project discovery response.');
  }
  if (page.has_more && (page.projects.length === 0 || typeof page.next_cursor !== 'string'
    || page.next_cursor !== page.projects[page.projects.length - 1] || page.next_cursor === after)) {
    throw new Error('Invalid project discovery cursor.');
  }
  return { projects: page.projects, has_more: page.has_more,
    ...(page.has_more ? { next_cursor: page.next_cursor as string } : {}) };
}

/** listProjects uses only the active credential; search and cursors cannot select a tenant. */
export async function listProjects(apiKey: string, query: string, after: string, signal: AbortSignal): Promise<ProjectPage> {
  if (!apiKey.trim()) throw new Error('API key is required.');
  if (!isProjectQuery(query) || (after !== '' && !projectID.test(after))) throw new Error('Invalid project search.');
  const page = await callFileAPI<unknown>(apiKey, 'GET', '/projects', {
    query: { q: query, after, limit: String(PROJECT_PAGE_SIZE) }, signal,
  });
  return parseProjectPage(page, after);
}
