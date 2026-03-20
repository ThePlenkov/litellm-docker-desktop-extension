import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: './',
  resolve: {
    preserveSymlinks: true,
  },
  optimizeDeps: {
    include: [
      '@mui/material',
      '@emotion/react',
      '@emotion/styled',
      '@docker/docker-mui-theme',
    ],
  },
  worker: {
    // Ensure web workers (monaco-yaml yaml.worker) are bundled as ES modules
    format: 'es',
  },
})
