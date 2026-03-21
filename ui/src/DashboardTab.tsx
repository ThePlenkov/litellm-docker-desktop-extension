import { useCallback, useEffect, useState } from 'react';
import {
  Box, Button, CircularProgress, Stack, Tooltip, Typography, Link,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { v1 } from '@docker/extension-api-client-types';

const PROXY_URL = 'http://localhost:4000';
const CONFIG_URL = 'http://localhost:4001';

/* ---------- types (shared with SecretsTab) ---------- */

interface SecretEntry { mode: 'direct' | 'command'; value: string }
interface SecretsConfig { secrets: Record<string, SecretEntry> }

interface Props {
  ddClient: v1.DockerDesktopClient;
  healthy: boolean | null;
}

export function DashboardTab({ ddClient, healthy }: Props) {
  const toast = ddClient.desktopUI.toast;

  const open = (url: string) => {
    try { ddClient.host.openExternal(url); } catch { window.open(url, '_blank'); }
  };

  /* ---- detect command-mode secrets ---- */
  const [commandSecrets, setCommandSecrets] = useState<[string, string][]>([]); // [key, command]
  const [refreshing, setRefreshing] = useState(false);

  const loadConfig = useCallback(async () => {
    try {
      const res = await fetch(`${CONFIG_URL}/secrets-config`);
      if (!res.ok) return;
      const cfg: SecretsConfig = await res.json();
      const cmds = Object.entries(cfg.secrets ?? {})
        .filter(([, e]) => e.mode === 'command' && e.value.trim())
        .map(([k, e]) => [k, e.value] as [string, string]);
      setCommandSecrets(cmds);
    } catch { /* config-server not ready yet */ }
  }, []);

  useEffect(() => { loadConfig(); }, [loadConfig]);

  /* ---- refresh all command-mode secrets ---- */
  const handleRefresh = async () => {
    setRefreshing(true);
    const errors: string[] = [];
    let saved = 0;

    for (const [key, command] of commandSecrets) {
      try {
        const result = await ddClient.extension.host?.cli.exec('secret-helper', [command.trim()]);
        if (!result || (result.code && result.code !== 0)) {
          errors.push(`${key}: ${result?.stderr || 'host binary error'}`);
          continue;
        }
        const resolved = result.stdout.trim();
        if (!resolved) { errors.push(`${key}: command returned empty output`); continue; }

        const saveRes = await fetch(`${CONFIG_URL}/secrets/${key}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ value: resolved }),
        });
        if (!saveRes.ok) { errors.push(`${key}: save failed (HTTP ${saveRes.status})`); continue; }
        saved++;
      } catch (err: any) {
        errors.push(`${key}: ${err.message || err}`);
      }
    }

    if (saved > 0) {
      toast.warning('Restarting LiteLLM...');
      try {
        const containers = (await ddClient.docker.listContainers()) as Array<{ Id: string; Image: string }>;
        const llm = containers.find(c => c.Image?.includes('litellm') && !c.Image?.includes('config-server'));
        if (llm) {
          await ddClient.docker.cli.exec('container', ['restart', llm.Id]);
          toast.success(`Refreshed ${saved} secret(s) & restarted LiteLLM`);
        } else {
          toast.warning(`Refreshed ${saved} secret(s) but LiteLLM container not found`);
        }
      } catch (err: any) {
        toast.error(`Secrets refreshed but restart failed: ${err.message || err}`);
      }
    }

    if (errors.length) {
      toast.error(`Errors:\n${errors.join('\n')}`);
    }

    setRefreshing(false);
  };

  const hasCommandSecrets = commandSecrets.length > 0;

  return (
    <Box sx={{ textAlign: 'center', py: 6 }}>
      <Typography variant="h3" fontWeight={700} gutterBottom>
        <Box component="span" sx={{ color: '#e94560' }}>Lite</Box>
        <Box component="span" sx={{ color: '#0f3460' }}>LLM</Box>
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 4 }}>
        OpenAI-compatible LLM proxy
      </Typography>

      <Stack spacing={1.5} alignItems="center">
        <Button
          variant="contained"
          disabled={!healthy}
          onClick={() => open(`${PROXY_URL}/ui`)}
          sx={{ width: 240, bgcolor: '#e94560', '&:hover': { bgcolor: '#d13354' } }}
        >
          Open LiteLLM UI
        </Button>
        <Button
          variant="outlined"
          disabled={!healthy}
          onClick={() => open(PROXY_URL)}
          sx={{ width: 240 }}
        >
          Open API Docs
        </Button>

        {hasCommandSecrets && (
          <Tooltip
            title={`Re-run host commands for: ${commandSecrets.map(([k]) => k).join(', ')}`}
            arrow
          >
            <span>
              <Button
                variant="outlined"
                color="warning"
                disabled={refreshing}
                onClick={handleRefresh}
                startIcon={refreshing ? <CircularProgress size={16} /> : <RefreshIcon />}
                sx={{ width: 240, mt: 1 }}
              >
                {refreshing ? 'Refreshing...' : 'Refresh API Token'}
              </Button>
            </span>
          </Tooltip>
        )}
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ mt: 4, display: 'block' }}>
        Proxy at{' '}
        <Link
          component="button"
          variant="caption"
          onClick={() => open(PROXY_URL)}
          sx={{ verticalAlign: 'baseline' }}
        >
          {PROXY_URL}
        </Link>
      </Typography>
    </Box>
  );
}
