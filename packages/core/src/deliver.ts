// Rules for what an agent may see and what it gets delivered. The hub applies them to every
// event before it leaves the VPS, so an agent never receives a direct message between others.
import { type Address, BROADCAST, sameAddress } from './names.js'
import type { BusEvent, Sender, To } from './schema.js'

function isMe(x: Sender | To, me: Address): boolean {
  return typeof x !== 'string' && sameAddress(x, me)
}

/** True if `me` may see this message in history: sent to all, sent to me, or sent by me. */
export function isVisible(e: BusEvent, me: Address): boolean {
  return e.kind === 'msg' && (e.to === BROADCAST || isMe(e.to, me) || isMe(e.from, me))
}

export type Delivery = 'message' | 'notice'

/**
 * What the hub pushes to `me` for one event:
 * - 'message': a message from someone else, sent to all or to me.
 * - 'notice': my own kick, or the redact of a message that `me` already got.
 * - undefined: nothing.
 */
export function deliveryFor(
  e: BusEvent,
  me: Address,
  wasDelivered: (id: string) => boolean,
): Delivery | undefined {
  switch (e.kind) {
    case 'msg':
      return !isMe(e.from, me) && (e.to === BROADCAST || isMe(e.to, me)) ? 'message' : undefined
    case 'kick':
      return sameAddress(e.target, me) ? 'notice' : undefined
    case 'redact':
      return wasDelivered(e.id) ? 'notice' : undefined
    case 'evt':
      return undefined
  }
}
