import { defineConfig } from 'vitest/config'

// The "@coop/source" export condition makes tests import workspace packages from src, not dist.
export default defineConfig({
  resolve: { conditions: ['@coop/source'] },
  test: {
    passWithNoTests: true,
    // The TUI shows clock times in the operator's zone; snapshots are taken in UTC.
    env: { TZ: 'UTC' },
    include: ['packages/*/test/**/*.test.ts', 'packages/*/test/**/*.test.tsx'],
  },
})
