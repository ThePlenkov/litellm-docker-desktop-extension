import { Box, Button, Stack, Typography, Link } from '@mui/material';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';
import type { v1 } from '@docker/extension-api-client-types';

const PROXY_URL = 'http://localhost:4000';

interface Props {
  ddClient: v1.DockerDesktopClient;
  healthy: boolean | null;
}

export function DashboardTab({ ddClient, healthy }: Props) {
  const open = (url: string) => {
    try { ddClient.host.openExternal(url); } catch { window.open(url, '_blank'); }
  };

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
          endIcon={<OpenInNewIcon />}
          disabled={!healthy}
          onClick={() => open(`${PROXY_URL}/ui`)}
          sx={{ width: 240, bgcolor: '#e94560', '&:hover': { bgcolor: '#d13354' } }}
        >
          Open LiteLLM UI
        </Button>
        <Button
          variant="outlined"
          endIcon={<OpenInNewIcon />}
          disabled={!healthy}
          onClick={() => open(PROXY_URL)}
          sx={{ width: 240 }}
        >
          Open API Docs
        </Button>
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
        {' '}&middot; Key: <code>sk-1234</code>
      </Typography>
    </Box>
  );
}
