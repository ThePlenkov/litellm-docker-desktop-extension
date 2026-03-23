import { useEffect, useRef, useCallback } from 'react';
import type { v1 } from '@docker/extension-api-client-types';

const CONFIG_URL = 'http://localhost:4001';
const POLL_INTERVAL = 30_000; // 30 seconds

/**
 * Patterns that indicate credential / auth failures — NOT rate limits (429)
 * or generic server errors (500).
 */
const AUTH_ERROR_PATTERNS = [
  /\b401\b/,
  /\b403\b/,
  /AuthenticationError/i,
  /Unauthorized/i,
  /invalid.?api.?key/i,
  /incorrect.?api.?key/i,
  /permission.?denied/i,
  /access.?denied/i,
];

interface HealthEndpoint {
  model?: string;
  model_name?: string;
  error?: string;
  [key: string]: unknown;
}

interface HealthResponse {
  healthy_endpoints?: HealthEndpoint[];
  unhealthy_endpoints?: HealthEndpoint[];
}

function isAuthError(msg: string): boolean {
  return AUTH_ERROR_PATTERNS.some(p => p.test(msg));
}

/** Stable key per model so we only alert once per unique failure. */
function fingerprint(ep: HealthEndpoint): string {
  return `auth:${ep.model_name || ep.model || 'unknown'}`;
}

/**
 * Monitors LiteLLM model-level health for credential errors.
 *
 * - Polls /litellm-model-health every 30 s (only while LiteLLM is up).
 * - Fires ONE toast per batch of *new* auth errors; already-notified
 *   fingerprints are suppressed so the user's overnight desktop doesn't
 *   fill with repeated toasts.
 * - When a previously-failing model becomes healthy again its fingerprint
 *   is cleared, so a *new* expiry later will re-trigger a notification.
 * - If command-mode secrets exist, attempts an automatic refresh + restart
 *   before asking the user to intervene manually.
 */
export function useAuthMonitor(
  ddClient: v1.DockerDesktopClient | null,
  healthy: boolean | null,
) {
  const notifiedRef = useRef<Set<string>>(new Set());
  const refreshingRef = useRef(false);

  const check = useCallback(async () => {
    if (!healthy || !ddClient) return;

    try {
      const res = await fetch(`${CONFIG_URL}/litellm-model-health`, {
        signal: AbortSignal.timeout(15_000),
      });
      if (!res.ok) return;

      const data: HealthResponse = await res.json();
      const unhealthy = data.unhealthy_endpoints ?? [];

      // Identify auth-related failures
      const authErrors = unhealthy.filter(ep => ep.error && isAuthError(ep.error));
      const currentFPs = new Set(authErrors.map(fingerprint));

      // Forget resolved errors so a re-expiry triggers a fresh notification
      for (const fp of notifiedRef.current) {
        if (!currentFPs.has(fp)) notifiedRef.current.delete(fp);
      }

      // Only act on errors we haven't notified about yet
      const newErrors = authErrors.filter(ep => !notifiedRef.current.has(fingerprint(ep)));
      if (newErrors.length === 0) return;

      // Mark as notified immediately (before any async work) to avoid races
      for (const ep of newErrors) notifiedRef.current.add(fingerprint(ep));

      const models = [...new Set(newErrors.map(ep => ep.model_name || ep.model || 'unknown'))];
      const modelList = models.join(', ');

      // Check for command-mode secrets that can be auto-refreshed
      let hasCommandSecrets = false;
      try {
        const cfgRes = await fetch(`${CONFIG_URL}/secrets-config`);
        if (cfgRes.ok) {
          const cfg = await cfgRes.json();
          hasCommandSecrets = Object.values(cfg.secrets ?? {}).some(
            (e: any) => e.mode === 'command' && e.value?.trim(),
          );
        }
      } catch { /* ignore */ }

      if (hasCommandSecrets && !refreshingRef.current) {
        ddClient.desktopUI.toast.warning(
          `Credential error for ${modelList} — auto-refreshing secrets\u2026`,
        );
        refreshingRef.current = true;
        try {
          await autoRefreshSecrets(ddClient);
          ddClient.desktopUI.toast.success('Secrets refreshed & LiteLLM restarted.');
          // Clear fingerprints — next poll will verify whether the new creds work
          notifiedRef.current.clear();
        } catch (err: any) {
          ddClient.desktopUI.toast.error(
            `Auto-refresh failed: ${err.message || err}. Update secrets in the Secrets tab.`,
          );
        } finally {
          refreshingRef.current = false;
        }
      } else if (!hasCommandSecrets) {
        ddClient.desktopUI.toast.error(
          `Credential error for: ${modelList}. Check the Secrets tab to update your API keys.`,
        );
      }
      // If hasCommandSecrets && refreshingRef.current → a refresh is already in flight, skip
    } catch {
      // LiteLLM or config-server unreachable — ignore, liveliness poll handles that
    }
  }, [ddClient, healthy]);

  useEffect(() => {
    if (!healthy || !ddClient) return;

    // Wait a bit after LiteLLM reports healthy before first model-health check
    const initial = setTimeout(check, 10_000);
    const id = setInterval(check, POLL_INTERVAL);
    return () => { clearTimeout(initial); clearInterval(id); };
  }, [check, healthy, ddClient]);
}

/* ------------------------------------------------------------------ */
/*  Auto-refresh helper (same logic as DashboardTab.handleRefresh)    */
/* ------------------------------------------------------------------ */

async function autoRefreshSecrets(ddClient: v1.DockerDesktopClient) {
  const cfgRes = await fetch(`${CONFIG_URL}/secrets-config`);
  if (!cfgRes.ok) throw new Error('Could not load secrets config');
  const cfg = await cfgRes.json();

  const commandSecrets = Object.entries(cfg.secrets ?? {})
    .filter(([, e]: [string, any]) => e.mode === 'command' && e.value?.trim())
    .map(([k, e]: [string, any]) => [k, e.value] as [string, string]);

  if (commandSecrets.length === 0) return;

  let saved = 0;
  for (const [key, command] of commandSecrets) {
    try {
      const result = await ddClient.extension.host?.cli.exec('secret-helper', [command.trim()]);
      if (!result || (result.code && result.code !== 0)) continue;
      const resolved = result.stdout.trim();
      if (!resolved) continue;

      const saveRes = await fetch(`${CONFIG_URL}/secrets/${key}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ value: resolved }),
      });
      if (saveRes.ok) saved++;
    } catch { /* skip this secret */ }
  }

  if (saved > 0) {
    const containers = (await ddClient.docker.listContainers()) as Array<{
      Id: string;
      Image: string;
    }>;
    const llm = containers.find(
      c => c.Image?.includes('litellm') && !c.Image?.includes('config-server'),
    );
    if (llm) {
      await ddClient.docker.cli.exec('container', ['restart', llm.Id]);
    }
  }
}
