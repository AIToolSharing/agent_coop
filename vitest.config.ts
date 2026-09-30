import { defineConfig } from 'vitest/config'

// The "source" export condition makes tests import workspace packages from src, not dist.
export default defineConfig({
  resolve: { conditions: ['source'] },
  test: {
    passWithNoTests: true,
    include: ['packages/*/test/**/*.test.ts', 'packages/*/test/**/*.test.tsx'],
  },
})
