// Read server-sent events from a response body, with a watchdog: a body silent for `idleMs`
// (a lost connection shows no error) is aborted through `idle`.
import { createParser, type EventSourceMessage } from 'eventsource-parser'

export type { EventSourceMessage }

export async function* readSse(
  body: ReadableStream<Uint8Array>,
  idleMs: number,
  idle: AbortController,
): AsyncGenerator<EventSourceMessage> {
  const buffer: EventSourceMessage[] = []
  const parser = createParser({ onEvent: (e) => void buffer.push(e) })
  const decoder = new TextDecoder()
  let timer: ReturnType<typeof setTimeout> | undefined
  const arm = () => {
    clearTimeout(timer)
    timer = setTimeout(() => idle.abort(), idleMs)
  }
  arm()
  try {
    for await (const chunk of body) {
      arm()
      parser.feed(decoder.decode(chunk, { stream: true }))
      yield* buffer.splice(0)
    }
  } finally {
    clearTimeout(timer)
  }
}

/** A sleep that `signal` cuts short. */
export function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(t)
        resolve()
      },
      { once: true },
    )
  })
}
