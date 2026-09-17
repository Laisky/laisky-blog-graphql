import { useEffect, useRef, useState } from 'react';
import { callFileTool } from './client';
import { canonicalFilePath, fileIOErrorMessage, fileSnapshot, RequestEpoch, type FileSnapshot, type ReadPayload } from './version-state';

/** Each lane has latest-request-wins ordering and ignores responses after unmount. */
export function useFileRequestLane() {
  const epochs = useRef(new Map<string, RequestEpoch>()).current;
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; epochs.forEach((epoch) => epoch.invalidate()); };
  }, [epochs]);
  return {
    cancel: () => epochs.forEach((epoch) => epoch.invalidate()),
    begin: (key = '') => {
      let epoch = epochs.get(key);
      if (!epoch) { epoch = new RequestEpoch(); epochs.set(key, epoch); }
      const requestEpoch = epoch;
      const ticket = requestEpoch.next();
      return () => alive.current && requestEpoch.current(ticket);
    },
  };
}

/** The caller clears this observation immediately when its path input changes. */
export function useFileSnapshot(apiKey: string, project: string, path: string) {
  const lane = useFileRequestLane();
  const [snapshot, setSnapshot] = useState<FileSnapshot | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  function clear() {
    lane.cancel();
    setSnapshot(null);
    setPending(false);
    setError(null);
  }
  async function load() {
    const current = lane.begin();
    setSnapshot(null);
    setError(null);
    setPending(true);
    try {
      const payload = await callFileTool<ReadPayload>(apiKey, 'file_read', {
        project, path: canonicalFilePath(path), offset: 0, length: -1,
      });
      if (current()) setSnapshot(fileSnapshot(path, payload));
    } catch (err) {
      if (current()) setError(fileIOErrorMessage(err));
    } finally {
      if (current()) setPending(false);
    }
  }
  return { snapshot: snapshot?.path === canonicalFilePath(path) ? snapshot : null, pending, error, clear, load };
}
