import { useEffect, useState } from 'react';
import type { v1 } from '@docker/extension-api-client-types';

export function useHealthCheck(
  ddClient: v1.DockerDesktopClient,
  intervalMs = 5000,
): boolean | null {
  const [healthy, setHealthy] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;

    async function check() {
      try {
        await ddClient.extension.vm?.service?.get(':4000/health/liveliness');
        if (active) setHealthy(true);
      } catch {
        if (active) setHealthy(false);
      }
    }

    check();
    const id = setInterval(check, intervalMs);
    return () => { active = false; clearInterval(id); };
  }, [ddClient, intervalMs]);

  return healthy;
}
