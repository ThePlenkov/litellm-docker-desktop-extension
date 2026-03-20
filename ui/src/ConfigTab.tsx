import { useEffect, useState, useCallback } from 'react';
import {
  Box, Button, Stack, Typography, Link, TextField,
} from '@mui/material';
import SaveIcon from '@mui/icons-material/Save';
import RestartAltIcon from '@mui/icons-material/RestartAlt';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { v1 } from '@docker/extension-api-client-types';

interface Props {
  ddClient: v1.DockerDesktopClient;
}

export function ConfigTab({ ddClient }: Props) {
  const [config, setConfig] = useState('');
  const [status, setStatus] = useState('');
  const [saving, setSaving] = useState(false);

  const toast = ddClient.desktopUI.toast;

  const loadConfig = useCallback(async () => {
    setStatus('Loading...');
    try {
      const res = await ddClient.extension.vm?.service?.get(':4001/config');
      const text = typeof res === 'string' ? res : JSON.stringify(res, null, 2);
      setConfig(text);
      setStatus('Loaded');
    } catch (err: any) {
      setStatus(`Failed to load: ${err.message || err}`);
    }
  }, [ddClient]);

  useEffect(() => { loadConfig(); }, [loadConfig]);

  const saveConfig = async (): Promise<boolean> => {
    setSaving(true);
    try {
      await ddClient.extension.vm?.service?.post(':4001/config', config);
      toast.success('Config saved');
      setStatus(`Saved at ${new Date().toLocaleTimeString()}`);
      return true;
    } catch (err: any) {
      toast.error(`Save failed: ${err.message || err}`);
      return false;
    } finally {
      setSaving(false);
    }
  };

  const restartLiteLLM = async () => {
    toast.warning('Restarting LiteLLM...');
    try {
      const containers = (await ddClient.docker.listContainers()) as Array<{
        Id: string;
        Image: string;
      }>;
      const litellm = containers.find(
        (c) => c.Image?.includes('litellm') && !c.Image?.includes('config-server'),
      );
      if (litellm) {
        await ddClient.docker.cli.exec('container', ['restart', litellm.Id]);
        toast.success('LiteLLM restarting with new config');
      } else {
        toast.error('LiteLLM container not found');
      }
    } catch (err: any) {
      toast.error(`Restart failed: ${err.message || err}`);
    }
  };

  const handleApply = async () => {
    if (await saveConfig()) await restartLiteLLM();
  };

  return (
    <Box sx={{ py: 2 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 1 }}>
        <Typography variant="body2" color="text.secondary">
          config.yaml &mdash;{' '}
          <Link
            component="button"
            variant="body2"
            onClick={() => {
              try { ddClient.host.openExternal('https://docs.litellm.ai/docs/proxy/configs'); }
              catch { window.open('https://docs.litellm.ai/docs/proxy/configs', '_blank'); }
            }}
          >
            docs
          </Link>
        </Typography>
        <Stack direction="row" spacing={1}>
          <Button size="small" startIcon={<RefreshIcon />} onClick={loadConfig}>
            Reload
          </Button>
          <Button
            size="small"
            variant="contained"
            color="success"
            startIcon={<SaveIcon />}
            disabled={saving}
            onClick={saveConfig}
          >
            Save
          </Button>
          <Button
            size="small"
            variant="contained"
            color="warning"
            startIcon={<RestartAltIcon />}
            disabled={saving}
            onClick={handleApply}
          >
            Save & Restart
          </Button>
        </Stack>
      </Stack>

      <TextField
        multiline
        fullWidth
        minRows={22}
        value={config}
        onChange={(e) => setConfig(e.target.value)}
        spellCheck={false}
        InputProps={{
          sx: {
            fontFamily: '"SF Mono", "Fira Code", "Cascadia Code", Consolas, monospace',
            fontSize: '0.85rem',
            lineHeight: 1.55,
          },
        }}
        sx={{ '& .MuiOutlinedInput-root': { borderRadius: 2 } }}
      />

      <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
        {status}
      </Typography>
    </Box>
  );
}
