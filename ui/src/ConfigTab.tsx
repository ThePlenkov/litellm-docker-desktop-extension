import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Box, Button, Stack, Typography, Link, useTheme,
} from '@mui/material';
import type { v1 } from '@docker/extension-api-client-types';
import * as monaco from 'monaco-editor';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import YamlWorker from 'monaco-yaml/yaml.worker?worker';
import { configureMonacoYaml } from 'monaco-yaml';
import litellmSchema from './litellm-schema.json';

const CONFIG_URL = 'http://localhost:4001';

// Worker setup — must happen before any Monaco editor is created.
// Follows the official monaco-yaml Vite example exactly.
window.MonacoEnvironment = {
  getWorker(_moduleId: string, label: string) {
    switch (label) {
      case 'editorWorkerService':
        return new EditorWorker();
      case 'yaml':
        return new YamlWorker();
      default:
        throw new Error(`Unknown label ${label}`);
    }
  },
};

// Configure YAML language service with LiteLLM config schema.
configureMonacoYaml(monaco, {
  validate: true,
  completion: true,
  hover: true,
  format: true,
  schemas: [
    {
      uri: 'https://docs.litellm.ai/docs/proxy/config_settings',
      fileMatch: ['**/config.yaml'],
      schema: litellmSchema as any,
    },
  ],
});

const MODEL_URI = monaco.Uri.parse('file:///config.yaml');

interface Props {
  ddClient: v1.DockerDesktopClient;
}

export function ConfigTab({ ddClient }: Props) {
  const [status, setStatus] = useState('');
  const [saving, setSaving] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const modelRef = useRef<monaco.editor.ITextModel | null>(null);
  const theme = useTheme();
  const isDark = theme.palette.mode === 'dark';

  const toast = ddClient.desktopUI.toast;

  // Get or create the model with the correct URI for schema matching
  const getModel = useCallback(() => {
    if (!modelRef.current) {
      const existing = monaco.editor.getModel(MODEL_URI);
      modelRef.current = existing ?? monaco.editor.createModel('', 'yaml', MODEL_URI);
    }
    return modelRef.current;
  }, []);

  const loadConfig = useCallback(async () => {
    setStatus('Loading...');
    try {
      const res = await fetch(`${CONFIG_URL}/config`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const text = await res.text();
      getModel().setValue(text);
      setStatus('Loaded');
    } catch (err: any) {
      setStatus(`Failed to load: ${err.message || err}`);
    }
  }, [getModel]);

  // Create the Monaco editor instance (raw, no @monaco-editor/react)
  useEffect(() => {
    if (!containerRef.current) return;
    const model = getModel();
    const editor = monaco.editor.create(containerRef.current, {
      model,
      theme: isDark ? 'vs-dark' : 'light',
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      lineNumbers: 'on',
      scrollBeyondLastLine: false,
      wordWrap: 'on',
      tabSize: 2,
      padding: { top: 8 },
      quickSuggestions: {
        other: true,
        comments: false,
        strings: true,
      },
      suggestOnTriggerCharacters: true,
    });
    editorRef.current = editor;
    loadConfig();
    return () => editor.dispose();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Sync theme changes
  useEffect(() => {
    monaco.editor.setTheme(isDark ? 'vs-dark' : 'light');
  }, [isDark]);

  const getEditorValue = () => getModel().getValue();

  const saveConfig = async (): Promise<boolean> => {
    setSaving(true);
    try {
      const res = await fetch(`${CONFIG_URL}/config`, {
        method: 'POST',
        headers: { 'Content-Type': 'text/plain' },
        body: getEditorValue(),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
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
          <Button size="small" onClick={loadConfig}>
            Reload
          </Button>
          <Button
            size="small"
            variant="contained"
            color="success"
            disabled={saving}
            onClick={saveConfig}
          >
            Save
          </Button>
          <Button
            size="small"
            variant="contained"
            color="warning"
            disabled={saving}
            onClick={handleApply}
          >
            Save &amp; Restart
          </Button>
        </Stack>
      </Stack>

      <Box sx={{
        border: 1,
        borderColor: 'divider',
        borderRadius: 2,
        overflow: 'hidden',
        height: '60vh',
      }}>
        <div ref={containerRef} style={{ width: '100%', height: '100%' }} />
      </Box>

      <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
        {status}
      </Typography>
    </Box>
  );
}
