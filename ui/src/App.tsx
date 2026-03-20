import { useState } from 'react';
import { Box, Tab, Tabs, Stack, Typography, Chip } from '@mui/material';
import { createDockerDesktopClient } from '@docker/extension-api-client';
import { DashboardTab } from './DashboardTab';
import { ConfigTab } from './ConfigTab';
import { useHealthCheck } from './useHealthCheck';

const ddClient = createDockerDesktopClient();

export function App() {
  const [tab, setTab] = useState(0);
  const healthy = useHealthCheck();

  return (
    <Box sx={{ width: '100%', maxWidth: 900, mx: 'auto' }}>
      {/* Header */}
      <Stack direction="row" alignItems="center" spacing={1.5} sx={{ px: 2, pt: 2, pb: 1 }}>
        <Typography variant="h5" fontWeight={700}>
          <Box component="span" sx={{ color: '#e94560' }}>Lite</Box>
          <Box component="span" sx={{ color: '#0f3460' }}>LLM</Box>
        </Typography>
        <Chip
          size="small"
          label={healthy === null ? 'Checking...' : healthy ? 'Running' : 'Starting...'}
          color={healthy === null ? 'default' : healthy ? 'success' : 'warning'}
          variant="outlined"
        />
      </Stack>

      {/* Tabs */}
      <Box sx={{ borderBottom: 1, borderColor: 'divider' }}>
        <Tabs value={tab} onChange={(_, v) => setTab(v)}>
          <Tab label="Dashboard" />
          <Tab label="Configuration" />
        </Tabs>
      </Box>

      {/* Tab panels */}
      {tab === 0 && <DashboardTab ddClient={ddClient} healthy={healthy} />}
      {tab === 1 && <ConfigTab ddClient={ddClient} />}
    </Box>
  );
}
