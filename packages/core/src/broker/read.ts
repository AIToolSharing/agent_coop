// Read and follow stream COOP with ordered consumers. An ordered consumer recreates itself at the
// next expected sequence after a gap or a reconnect, so a follower sees each event once, in order.
import {
  DeliverPolicy,
  type JetStreamClient,
  type JetStreamManager,
  type JsMsg,
} from '@nats-io/jetstream'
import { STREAM_NAME } from '../names.js'
import { type BusEvent, type BusEventInput, decodeBusEvent, toWire } from '../schema.js'

/** Publish one event and return its stream sequence (the message id). */
export async function publishEvent(js: JetStreamClient, e: BusEventInput): Promise<number> {
  const w = toWire(e)
  const ack = await js.publish(w.subject, w.data)
  return ack.seq
}

export async function lastSeq(jsm: JetStreamManager): Promise<number> {
  return (await jsm.streams.info(STREAM_NAME)).state.last_seq
}

function decodeMsg(m: JsMsg): BusEvent | undefined {
  return decodeBusEvent(m.subject, m.data, m.seq)
}

async function orderedConsumer(js: JetStreamClient, filters: string[], fromSeq: number) {
  return js.consumers.get(STREAM_NAME, {
    filter_subjects: filters,
    deliver_policy: DeliverPolicy.StartSequence,
    opt_start_seq: Math.max(1, fromSeq),
  })
}

/** All events that match `filters` from `fromSeq` to `toSeq` (default: the end of the stream). */
export async function readRange(
  js: JetStreamClient,
  jsm: JetStreamManager,
  filters: string[],
  fromSeq = 1,
  toSeq?: number,
): Promise<BusEvent[]> {
  const end = toSeq ?? (await lastSeq(jsm))
  if (fromSeq > end) return []
  const c = await orderedConsumer(js, filters, fromSeq)
  const out: BusEvent[] = []
  // Fetch in batches; a short batch or pending 0 means the range is read.
  for (;;) {
    const batch = await c.fetch({ max_messages: 500, expires: 1000 })
    let n = 0
    let pending = 0
    for await (const m of batch) {
      n++
      pending = m.info.pending
      if (m.seq > end) return out
      const e = decodeMsg(m)
      if (e !== undefined) out.push(e)
    }
    if (n === 0 || pending === 0) return out
  }
}

/** Follow events that match `filters` from `fromSeq`, live, until `signal` aborts. */
export async function* follow(
  js: JetStreamClient,
  filters: string[],
  fromSeq: number,
  signal: AbortSignal,
): AsyncGenerator<BusEvent> {
  const c = await orderedConsumer(js, filters, fromSeq)
  const iter = await c.consume()
  const stop = () => {
    iter.stop()
  }
  if (signal.aborted) stop()
  signal.addEventListener('abort', stop, { once: true })
  try {
    for await (const m of iter) {
      const e = decodeMsg(m)
      if (e !== undefined) yield e
    }
  } finally {
    signal.removeEventListener('abort', stop)
    iter.stop()
  }
}
