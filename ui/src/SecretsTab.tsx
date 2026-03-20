import { useEffect, useState, useCallback } from 'react';
import {
  Alert, Box, Button, Chip, Divider, FormControlLabel, Radio, RadioGroup,
  Stack, TextField, Typography,
} from '@mui/material';
import type { v1 } from '@docker/extension-api-client-types';

const CONFIG_URL = 'http://localhost:4001';

/* ---------- types ---------- */

interface SecretEntry { mode: 'direct' | 'command'; value: string }
interface SecretsConfig { secrets: Record<string, SecretEntry> }
interface SecretStatus { name: string; exists: boolean; masked?: string; length?: number }

/* ---------- per-secret metadata ---------- */

interface SecretDef {
  key: string;            // file name on volume & API path segment
  envVar: string;         // env var name injected at startup
  label: string;          // UI heading
  description: string;    // help text
  placeholder: { direct: string; command: string };
  defaultNote?: string;   // shown when not set
}

const SECRETS: SecretDef[] = [
  {
    key: 'api_key',
    envVar: 'LITELLM_API_KEY',
    label: 'API Key',
    description:
      'The API key used to authenticate with the upstream LLM provider. ' +
      'All models in the config reference os.environ/LITELLM_API_KEY.',
    placeholder: {
      direct: 'sk-proj-abc123...',
      command: 'vault kv get -field=api_key secret/genai/litellm',
    },
    defaultNote: 'Not set — models will fail until configured',
  },
  {
    key: 'base_url',
    envVar: 'LITELLM_BASE_URL',
    label: 'Base URL',
    description:
      'The base URL for the upstream LLM gateway. ' +
      'All models reference os.environ/LITELLM_BASE_URL.',
    placeholder: {
      direct: 'https://api.example.com/v1',
      command: 'echo https://api.example.com/v1',
    },
    defaultNote: 'Not set — models will fail until configured',
  },
  {
    key: 'master_key',
    envVar: 'LITELLM_MASTER_KEY',
    label: 'Master Key',
    description:
      'The admin key that authenticates requests to the LiteLLM proxy itself. ' +
      'Defaults to sk-1234 if not set.',
    placeholder: {
      direct: 'sk-my-secure-proxy-key',
      command: 'vault kv get -field=master_key secret/genai/litellm',
    },
    defaultNote: 'Using default sk-1234',
  },
];

/* ---------- props ---------- */

interface Props { ddClient: v1.DockerDesktopClient }

/* ========== SecretField — one card per secret ========== */

function SecretField({ def, ddClient, onRestarted }: {
  def: SecretDef;
  ddClient: v1.DockerDesktopClient;
  onRestarted?: () => void;
}) {
  const toast = ddClient.desktopUI.toast;

  const [mode, setMode] = useState<'direct' | 'command'>('direct');
  const [value, setValue] = useState('');
  const [status, setStatus] = useState<SecretStatus | null>(null);
  const [applying, setApplying] = useState(false);
  const [loading, setLoading] = useState(true);
  const [lastError, setLastError] = useState('');

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [cfgRes, stRes] = await Promise.all([
        fetch(`${CONFIG_URL}/secrets-config`),
        fetch(`${CONFIG_URL}/secrets/${def.key}`),
      ]);
      if (cfgRes.ok) {
        const cfg: SecretsConfig = await cfgRes.json();
        const entry = cfg.secrets?.[def.key];
        if (entry) { setMode(entry.mode); setValue(entry.value); }
      }
      if (stRes.ok) setStatus(await stRes.json());
    } catch { /* ignore */ }
    if (!silent) setLoading(false);
  }, [def.key]);

  useEffect(() => { load(); }, [load]);

  const resolveValue = async (): Promise<string | null> => {
    setLastError('');
    if (mode === 'direct') {
      if (!value.trim()) { setLastError('Enter a value'); return null; }
      return value.trim();
    }
    if (!value.trim()) { setLastError('Enter a command'); return null; }
    try {
      const result = await ddClient.extension.host?.cli.exec('secret-helper', [value.trim()]);
      if (!result) {
        setLastError('host?.cli.exec returned undefined — host binary may not be registered');
        return null;
      }
      if (result.code && result.code !== 0) {
        setLastError(`Command exit code ${result.code}\nstderr: ${result.stderr}\nstdout: ${result.stdout}`);
        return null;
      }
      const out = result.stdout.trim();
      if (!out) { setLastError('Command succeeded but stdout was empty'); return null; }
      return out;
    } catch (err: any) {
      setLastError(`Exec exception: ${err.message || JSON.stringify(err)}`);
      return null;
    }
  };

  const save = async (restart: boolean) => {
    setApplying(true);
    try {
      const resolved = await resolveValue();
      if (!resolved) { setApplying(false); return; }

      // Write resolved value
      const saveRes = await fetch(`${CONFIG_URL}/secrets/${def.key}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ value: resolved }),
      });
      if (!saveRes.ok) throw new Error(`HTTP ${saveRes.status}`);

      // Persist mode+value config (so UI remembers the command)
      const cfgRes = await fetch(`${CONFIG_URL}/secrets-config`);
      const existing: SecretsConfig = cfgRes.ok
        ? await cfgRes.json()
        : { secrets: {} };
      existing.secrets[def.key] = { mode, value };
      await fetch(`${CONFIG_URL}/secrets-config`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(existing),
      });

      if (restart) {
        toast.warning('Restarting LiteLLM...');
        const containers = (await ddClient.docker.listContainers()) as Array<{ Id: string; Image: string }>;
        const llm = containers.find(c => c.Image?.includes('litellm') && !c.Image?.includes('config-server'));
        if (llm) {
          await ddClient.docker.cli.exec('container', ['restart', llm.Id]);
          toast.success(`${def.label} saved & LiteLLM restarting`);
          onRestarted?.();
        } else {
          toast.warning(`${def.label} saved but LiteLLM container not found`);
        }
      } else {
        toast.success(`${def.label} saved. Restart LiteLLM to apply.`);
      }

      // Refresh status display without flashing (silent=true)
      await load(true);
    } catch (err: any) {
      toast.error(`Failed: ${err.message || err}`);
    } finally {
      setApplying(false);
    }
  };

  const handleDelete = async () => {
    try {
      await fetch(`${CONFIG_URL}/secrets/${def.key}`, { method: 'DELETE' });
      setStatus(null);
      toast.success(`${def.label} removed. Will use default on next restart.`);
    } catch (err: any) {
      toast.error(`Delete failed: ${err.message || err}`);
    }
  };

  if (loading) return null;

  return (
    <Box sx={{ mb: 3 }}>
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 0.5 }}>
        <Typography variant="subtitle1" fontWeight={600}>{def.label}</Typography>
        <Typography variant="caption" color="text.secondary" fontFamily="monospace">
          {def.envVar}
        </Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        {def.description}
      </Typography>

      {/* Current status */}
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1 }}>
        <Typography variant="body2" fontWeight={600}>Current:</Typography>
        {status?.exists ? (
          <>
            <Chip size="small" label="Set" color="success" variant="outlined" />
            <Typography variant="body2" fontFamily="monospace">{status.masked}</Typography>
          </>
        ) : (
          <Chip size="small" label={def.defaultNote || 'Not set'} color="warning" variant="outlined" />
        )}
      </Stack>

      {/* Error from last command execution */}
      {lastError && (
        <Alert severity="error" sx={{ mb: 1, whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: 12 }}>
          {lastError}
        </Alert>
      )}

      {/* Mode */}
      <RadioGroup
        row
        value={mode}
        onChange={(e) => { setMode(e.target.value as 'direct' | 'command'); setValue(''); }}
      >
        <FormControlLabel value="direct" control={<Radio size="small" />} label="Direct value" />
        <FormControlLabel value="command" control={<Radio size="small" />} label="Host command" />
      </RadioGroup>

      {/* Input */}
      <TextField
        fullWidth size="small"
        type={mode === 'direct' ? 'password' : 'text'}
        label={mode === 'direct' ? 'Value' : 'Shell command (runs on host)'}
        placeholder={mode === 'direct' ? def.placeholder.direct : def.placeholder.command}
        helperText={mode === 'command' ? 'stdout becomes the value. Runs via secret-helper on host.' : undefined}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        sx={{ my: 1 }}
        InputLabelProps={{ shrink: true }}
      />

      {/* Actions */}
      <Stack direction="row" spacing={1}>
        <Button
          variant="contained" color="success" size="small"
          disabled={applying || !value.trim()}
          onClick={() => save(false)}
        >
          {mode === 'command' ? 'Run & Save' : 'Save'}
        </Button>
        <Button
          variant="contained" color="warning" size="small"
          disabled={applying || !value.trim()}
          onClick={() => save(true)}
        >
          {mode === 'command' ? 'Run, Save & Restart' : 'Save & Restart'}
        </Button>
        {status?.exists && (
          <Button variant="outlined" color="error" size="small" disabled={applying} onClick={handleDelete}>
            Remove
          </Button>
        )}
        <Button size="small" onClick={() => load()} disabled={applying}>Reload</Button>
      </Stack>
    </Box>
  );
}

/* ========== SecretsTab ========== */

export function SecretsTab({ ddClient }: Props) {
  const [showInfo, setShowInfo] = useState(true);

  return (
    <Box sx={{ py: 2, px: 1, maxWidth: 720 }}>
      <Typography variant="h6" gutterBottom>Secrets</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Manage secrets referenced by your LiteLLM config via <code>os.environ/...</code>.
        Set values directly or use a host command to fetch them from a secrets manager.
        Values are stored on the shared Docker volume and injected at container startup.
      </Typography>

      {SECRETS.map((def, i) => (
        <Box key={def.key}>
          {i > 0 && <Divider sx={{ my: 2 }} />}
          <SecretField def={def} ddClient={ddClient} />
        </Box>
      ))}

      {showInfo && (
        <Alert severity="info" sx={{ mt: 2 }} onClose={() => setShowInfo(false)}>
          <Typography variant="body2">
            <strong>How it works:</strong> Each secret is written to <code>/data/secrets/&lt;name&gt;</code> on the
            shared volume. The LiteLLM entrypoint reads these files and exports them as environment
            variables before starting. Your config.yaml references them
            via <code>os.environ/LITELLM_API_KEY</code>, etc.
          </Typography>
        </Alert>
      )}
    </Box>
  );
}
