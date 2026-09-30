// Stream and bucket setup. Only privileged users (hub, operator) run it. It is idempotent, so
// both call it when they start.
import {
  DiscardPolicy,
  type JetStreamManager,
  RetentionPolicy,
  StorageType,
  type StreamConfig,
} from '@nats-io/jetstream'
import type { KV, Kvm } from '@nats-io/kv'
import { PRESENCE_BUCKET, SESSIONS_BUCKET, STREAM_NAME, TOKENS_BUCKET } from '../names.js'

/** Presence expires this long after the last heartbeat. */
export const PRESENCE_TTL_MS = 15_000
/** The hub refreshes presence this often. */
export const PRESENCE_HEARTBEAT_MS = 5_000

export const STREAM_CONFIG: Partial<StreamConfig> & { name: string } = {
  name: STREAM_NAME,
  subjects: ['coop.>'],
  storage: StorageType.File,
  retention: RetentionPolicy.Limits,
  discard: DiscardPolicy.Old,
  max_msg_size: 64 * 1024,
  num_replicas: 1,
  // A rollup header could purge the stream; a TTL header could hide a message early.
  allow_rollup_hdrs: false,
  allow_msg_ttl: false,
  // The operator deletes single messages (redact) and purges a deleted session.
  deny_delete: false,
  deny_purge: false,
  allow_direct: true,
}

export interface Buckets {
  readonly sessions: KV
  readonly presence: KV
  readonly tokens: KV
}

export async function ensureStream(jsm: JetStreamManager): Promise<void> {
  try {
    const si = await jsm.streams.info(STREAM_NAME)
    await jsm.streams.update(STREAM_NAME, { ...si.config, ...STREAM_CONFIG })
  } catch (err) {
    if (!isNotFound(err)) throw err
    await jsm.streams.add(STREAM_CONFIG)
  }
}

export async function ensureBuckets(kvm: Kvm): Promise<Buckets> {
  const [sessions, presence, tokens] = await Promise.all([
    kvm.create(SESSIONS_BUCKET, { history: 1, storage: StorageType.File }),
    kvm.create(PRESENCE_BUCKET, {
      history: 1,
      storage: StorageType.File,
      // A key expires when no heartbeat renews it; the server then emits a marker that
      // watchers see as PURGE.
      ttl: PRESENCE_TTL_MS,
      markerTTL: 5_000,
    }),
    kvm.create(TOKENS_BUCKET, { history: 1, storage: StorageType.File }),
  ])
  return { sessions, presence, tokens }
}

function isNotFound(err: unknown): boolean {
  return err instanceof Error && /not found/i.test(err.message)
}
