import { defineConfig } from 'vitest/config'

// The "@coop/source" export condition makes tests import workspace packages from src, not dist.
export default defineConfig({
  resolve: { conditions: ['@coop/source'] },
  test: {
    passWithNoTests: true,
    include: ['packages/*/test/**/*.test.ts', 'packages/*/test/**/*.test.tsx'],
  },
})
