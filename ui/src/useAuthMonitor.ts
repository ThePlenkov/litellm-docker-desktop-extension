import { useEffect, useRef, useCallback } from 'react';
import type { v1 } from '@docker/extension-api-client-types';

const CONFIG_URL = 'http://localhost:4001';
const MODEL_HEALTH_INTERVAL = 30_000; // 30 seconds
const TOKEN_CHECK_INTERVAL = 60_000; // 60 seconds

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
 * Monitors LiteLLM model-level health for credential errors and validates the
 * upstream API token directly.
 *
 * - Polls /litellm-model-health every 30 s for per-model auth errors.
 * - Polls /token-check every 60 s to validate the upstream API token.
 * - Notification-only — never restarts LiteLLM or auto-refreshes secrets.
 * - Each unique error (per-model or token-level) fires ONE toast;
 *   resolved errors are cleared so re-expiry triggers a fresh notification.
 */
export function useAuthMonitor(
  ddClient: v1.DockerDesktopClient | null,
  healthy: boolean | null,
) {
  const notifiedRef = useRef<Set<string>>(new Set());
  const tokenExpiredRef = useRef(false);

  // --- Per-model health check ---
  const checkModelHealth = useCallback(async () => {
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

      // Mark as notified immediately to avoid races
      for (const ep of newErrors) notifiedRef.current.add(fingerprint(ep));

      const models = [...new Set(newErrors.map(ep => ep.model_name || ep.model || 'unknown'))];
      const modelList = models.join(', ');

      ddClient.desktopUI.toast.error(
        `Credential error for: ${modelList}. Check the Secrets tab to update your API keys.`,
      );
    } catch {
      // LiteLLM or config-server unreachable — ignore, liveliness poll handles that
    }
  }, [ddClient, healthy]);

  // --- Direct upstream token validation ---
  const checkToken = useCallback(async () => {
    if (!ddClient) return;

    try {
      const res = await fetch(`${CONFIG_URL}/token-check`, {
        signal: AbortSignal.timeout(15_000),
      });
      if (!res.ok) return;

      const data: { valid: boolean | null; error?: string } = await res.json();

      if (data.valid === false && !tokenExpiredRef.current) {
        // Token just became invalid — notify once
        tokenExpiredRef.current = true;
        ddClient.desktopUI.toast.error(
          'API token is expired or invalid. Refresh it in the Secrets tab.',
        );
      } else if (data.valid === true && tokenExpiredRef.current) {
        // Token recovered — clear flag so next expiry re-triggers
        tokenExpiredRef.current = false;
        ddClient.desktopUI.toast.success('API token is valid again.');
      }
      // data.valid === null → secrets not configured or upstream unreachable, ignore
    } catch {
      // config-server unreachable — ignore
    }
  }, [ddClient]);

  useEffect(() => {
    if (!ddClient) return;

    // Token check runs even when LiteLLM is unhealthy (it hits upstream directly)
    const tokenInitial = setTimeout(checkToken, 5_000);
    const tokenId = setInterval(checkToken, TOKEN_CHECK_INTERVAL);

    // Model-health check only runs when LiteLLM is up
    let modelInitial: ReturnType<typeof setTimeout> | undefined;
    let modelId: ReturnType<typeof setInterval> | undefined;
    if (healthy) {
      modelInitial = setTimeout(checkModelHealth, 10_000);
      modelId = setInterval(checkModelHealth, MODEL_HEALTH_INTERVAL);
    }

    return () => {
      clearTimeout(tokenInitial);
      clearInterval(tokenId);
      if (modelInitial) clearTimeout(modelInitial);
      if (modelId) clearInterval(modelId);
    };
  }, [checkModelHealth, checkToken, healthy, ddClient]);
}
