/** Sessions soft-queued for warm-host capacity (resume returned queued: true). */

const queued = new Map<string, number>();
const listeners = new Set<() => void>();

function key(sessionId: string, hostId?: string | null): string {
  return `${hostId ?? "local"}:${sessionId}`;
}

function emit(): void {
  for (const listener of listeners) listener();
}

export function markCapacityWakeQueued(
  sessionId: string,
  hostId?: string | null,
): void {
  queued.set(key(sessionId, hostId), Date.now());
  emit();
}

export function clearCapacityWakeQueued(
  sessionId: string,
  hostId?: string | null,
): void {
  if (queued.delete(key(sessionId, hostId))) emit();
}

export function isCapacityWakeQueued(
  sessionId: string,
  hostId?: string | null,
): boolean {
  return queued.has(key(sessionId, hostId));
}

/** Subscribe to queue membership changes (for React). */
export function subscribeCapacityWakeQueued(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
