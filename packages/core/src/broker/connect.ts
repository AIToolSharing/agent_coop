// Connection for the privileged broker users (hub and operator).
import {
  type JetStreamClient,
  type JetStreamManager,
  jetstream,
  jetstreamManager,
} from '@nats-io/jetstream'
import { Kvm } from '@nats-io/kv'
import { connect, type NatsConnection } from '@nats-io/transport-node'
import { type Buckets, ensureBuckets, ensureStream } from './infra.js'

export interface BrokerOptions {
  readonly servers: string
  readonly user: string
  readonly pass: string
  /** Connection name, shown in the server's connection list. */
  readonly name: string
}

export interface Broker extends Buckets {
  readonly nc: NatsConnection
  readonly js: JetStreamClient
  readonly jsm: JetStreamManager
  close(): Promise<void>
}

/** Connect, then create or update the stream and buckets. */
export async function openBroker(o: BrokerOptions): Promise<Broker> {
  const nc = await connect({
    servers: o.servers,
    user: o.user,
    pass: o.pass,
    name: o.name,
    maxReconnectAttempts: -1,
  })
  try {
    const jsm = await jetstreamManager(nc)
    await ensureStream(jsm)
    const js = jetstream(nc)
    const buckets = await ensureBuckets(new Kvm(js))
    return { nc, js, jsm, ...buckets, close: () => nc.drain() }
  } catch (err) {
    await nc.close()
    throw err
  }
}
