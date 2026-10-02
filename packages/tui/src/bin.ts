#!/usr/bin/env node
// The entry of coop-tui. React must see NODE_ENV=production before it loads: its development
// build keeps memory on every repaint and ends a long session out of memory (Ink issue #869).
// A static import would load React first, so main.js is imported after the variable is set.
process.env.NODE_ENV ??= 'production'
await import('./main.js')
