'use client';

import { KubeQueueClient, type SystemStatus } from '@kubequeue/api-client';
import { useEffect, useState } from 'react';

const client = new KubeQueueClient();

export function SystemIndicator() {
  const [status, setStatus] = useState<SystemStatus>();
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let active = true;
    async function refresh() {
      try {
        const next = await client.getSystemStatus();
        if (active) setStatus(next);
      } catch {
        if (active) setStatus(undefined);
      } finally {
        if (active) setLoaded(true);
      }
    }
    void refresh();
    const interval = window.setInterval(() => void refresh(), 10_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, []);

  if (!loaded) {
    return (
      <span className="cluster-indicator" role="status">
        Checking worker…
      </span>
    );
  }
  const state = status?.worker.state ?? 'unavailable';
  const label =
    state === 'ready'
      ? 'Worker ready'
      : state === 'degraded'
        ? 'Worker degraded'
        : 'Worker unavailable';

  return (
    <span className={`cluster-indicator worker-${state}`} role="status">
      <i aria-hidden="true" />
      {label}
    </span>
  );
}
