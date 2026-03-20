import { useEffect, useState } from 'react';

const PROXY_URL = 'http://localhost:4000';

export function useHealthCheck(intervalMs = 5000): boolean | null {
  const [healthy, setHealthy] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;

    async function check() {
      try {
        const res = await fetch(`${PROXY_URL}/health/liveliness`, {
          signal: AbortSignal.timeout(3000),
        });
        if (active) setHealthy(res.ok);
      } catch {
        if (active) setHealthy(false);
      }
    }

    check();
    const id = setInterval(check, intervalMs);
    return () => { active = false; clearInterval(id); };
  }, [intervalMs]);

  return healthy;
}
