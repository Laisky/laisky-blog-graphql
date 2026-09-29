import { ChevronDown } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

import { isProjectQuery, listProjects, type ProjectPage } from './project-options';

type ProjectPickerProps = {
  id: string;
  apiKey: string;
  value: string;
  disabled?: boolean;
  onChange: (project: string) => boolean | void;
};
type SearchState = ProjectPage & { query: string; loading: boolean; failed: boolean };
const emptyPage: ProjectPage = { projects: [], has_more: false };
const maximumVisibleProjects = 500;

/** ProjectPicker isolates transient suggestions whenever its credential or lock changes. */
export function ProjectPicker(props: ProjectPickerProps) {
  return <ProjectPickerSession key={JSON.stringify([props.apiKey, Boolean(props.disabled)])} {...props} />;
}

/** ProjectPickerSession allows either explicit selection or an unlisted, manually entered ID. */
function ProjectPickerSession({ id, apiKey, value, disabled = false, onChange }: ProjectPickerProps) {
  const listID = useId();
  const input = useRef<HTMLInputElement>(null);
  const request = useRef<AbortController | null>(null);
  const [open, setOpen] = useState(false);
  const [browseAll, setBrowseAll] = useState(true);
  const [active, setActive] = useState(-1);
  const [state, setState] = useState<SearchState | null>(null);
  const [revision, setRevision] = useState(0);
  const unavailable = disabled || !apiKey;
  const query = browseAll ? '' : value.trim();
  const validQuery = isProjectQuery(query);
  // Never paint a previous query while its replacement is waiting for the debounce.
  const page = state?.query === query ? state : null;
  const projects = page?.projects ?? [];
  const loading = open && validQuery && (!page || page.loading);
  const expanded = open && !unavailable;
  const activeID = expanded && active >= 0 && active < projects.length ? `${listID}-${active}` : undefined;

  function cancel() {
    request.current?.abort();
    request.current = null;
  }
  function close() {
    cancel();
    setOpen(false);
    setActive(-1);
    setState(null);
  }
  function showAll() {
    if (unavailable) return;
    cancel();
    setState(null);
    setBrowseAll(true);
    setRevision((current) => current + 1);
    setActive(-1);
    setOpen(true);
    input.current?.focus();
  }
  function choose(project: string) {
    if (onChange(project) === false) return;
    close();
    input.current?.focus();
  }

  useEffect(() => {
    if (!expanded || !validQuery) return;
    const controller = new AbortController();
    request.current = controller;
    const timer = window.setTimeout(() => {
      setState({ ...emptyPage, query, loading: true, failed: false });
      void listProjects(apiKey, query, '', controller.signal).then((result) => {
        if (controller.signal.aborted || request.current !== controller) return;
        setState({ ...result, query, loading: false, failed: false });
      }).catch(() => {
        if (controller.signal.aborted || request.current !== controller) return;
        setState({ ...emptyPage, query, loading: false, failed: true });
      });
    }, 200);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
      // Also stop pagination, whose controller may have replaced the first request.
      request.current?.abort();
      request.current = null;
    };
  }, [apiKey, expanded, query, validQuery, revision]);

  useEffect(() => {
    if (activeID) document.getElementById(activeID)?.scrollIntoView?.({ block: 'nearest' });
  }, [activeID]);

  async function loadMore() {
    if (!page?.has_more || page.loading || !page.next_cursor || projects.length >= maximumVisibleProjects) return;
    cancel();
    const controller = new AbortController();
    request.current = controller;
    const previous = page;
    setState({ ...previous, loading: true, failed: false });
    try {
      const result = await listProjects(apiKey, query, previous.next_cursor!, controller.signal);
      if (controller.signal.aborted || request.current !== controller) return;
      const combined = [...new Set([...previous.projects, ...result.projects])];
      // Reject a non-progressing page instead of allowing an endless load-more loop.
      if (combined.length === previous.projects.length && result.has_more) throw new Error('Project pagination did not advance.');
      setState({ ...result, projects: combined, query, loading: false, failed: false });
    } catch {
      if (controller.signal.aborted || request.current !== controller) return;
      setState({ ...previous, loading: false, failed: true });
    }
  }

  return <div className="relative" onBlur={(event) => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) close();
  }}>
    <div className="relative">
      <Input ref={input} id={id} role="combobox" aria-autocomplete="list" aria-expanded={expanded}
        aria-controls={expanded ? listID : undefined} aria-activedescendant={activeID}
        aria-describedby={`${listID}-help`} autoComplete="off" autoCapitalize="none" spellCheck={false}
        maxLength={128} placeholder="Select or enter a project ID" required disabled={unavailable}
        className="pr-10" value={value} onFocus={() => { if (!open) showAll(); }}
        onChange={(event) => {
          if (onChange(event.target.value) === false) return;
          cancel(); setState(null); setActive(-1); setBrowseAll(false); setOpen(true);
          setRevision((current) => current + 1);
        }} onKeyDown={(event) => {
          if (event.nativeEvent.isComposing) return;
          if (event.key === 'Escape' && expanded) { event.preventDefault(); close(); }
          else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault();
            if (!expanded) { showAll(); return; }
            if (projects.length) setActive((index) => event.key === 'ArrowDown'
              ? (index + 1) % projects.length : (index < 0 ? projects.length - 1 : (index + projects.length - 1) % projects.length));
          } else if (event.key === 'Enter' && activeID) {
            event.preventDefault(); choose(projects[active]);
          }
        }} />
      <button type="button" tabIndex={-1} aria-label="Show existing projects" disabled={unavailable}
        className="absolute inset-y-0 right-0 flex w-10 items-center justify-center disabled:opacity-50"
        onMouseDown={(event) => event.preventDefault()} onClick={() => expanded ? close() : showAll()}>
        <ChevronDown className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
    <p id={`${listID}-help`} className="mt-1 text-xs text-muted-foreground">
      Choose a project belonging to the current API key, or type a new ID. Typing searches by character order, ignoring case.
    </p>
    {expanded && <div className="absolute z-50 mt-1 w-full rounded-md border bg-popover p-1 text-popover-foreground shadow-md">
      <ul id={listID} role="listbox" aria-label="Existing projects" aria-busy={loading} className="max-h-60 overflow-y-auto">
        {projects.map((project, index) => <li key={project} id={`${listID}-${index}`} role="option"
          aria-selected={active === index} className={cn('cursor-pointer break-all rounded-sm px-3 py-2 text-sm hover:bg-accent', active === index && 'bg-accent')}
          onMouseDown={(event) => event.preventDefault()} onMouseMove={() => setActive(index)} onClick={() => choose(project)}>
          {project}
        </li>)}
      </ul>
      <div role="status" className="px-3 py-2 text-xs text-muted-foreground">
        {!validQuery ? 'Use up to 128 letters, digits, dots, underscores, or hyphens.'
          : loading ? 'Loading projects...'
            : page?.failed ? 'Project suggestions are unavailable. You can still enter an ID manually.'
              : projects.length === 0 ? 'No matching projects. You can enter a new ID.'
                : `${projects.length} project${projects.length === 1 ? '' : 's'} shown.`}
      </div>
      {page?.has_more && (projects.length >= maximumVisibleProjects
        ? <p className="px-3 py-2 text-xs text-muted-foreground">Type a more specific search to find additional projects.</p>
        : <Button type="button" variant="ghost" size="sm" disabled={loading} onClick={() => void loadMore()}>Load more projects</Button>)}
    </div>}
  </div>;
}
