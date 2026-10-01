import type { ApiMessage, DeliveryVia } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import { Inbox, type Item, matchesPeer } from '../src/inbox.js'

const peer = fc.constantFrom('alice@m1', 'bob@m2', 'bob@m3', 'carol@m1')
const message = (i: number, from: string, reply_to?: string): ApiMessage => ({
  id: String(i + 1),
  from,
  to: 'all',
  text: `t${i}`,
  sent_at: '2026-09-30T00:00:00.000Z',
  ...(reply_to === undefined ? {} : { reply_to }),
})

function harness(push: boolean) {
  const pushed: Item[] = []
  const delivered: [string, DeliveryVia][] = []
  const inbox = new Inbox({
    push,
    onPush: async (it) => {
      pushed.push(it)
    },
    onDelivered: (id, via) => delivered.push([id, via]),
  })
  return { inbox, pushed, delivered }
}

describe('inbox routing', () => {
  test.prop([fc.array(peer, { maxLength: 50 })])(
    'pull mode: take returns every message once, in order, each delivered once',
    async (froms) => {
      const { inbox, pushed, delivered } = harness(false)
      const msgs = froms.map((f, i) => message(i, f))
      for (const m of msgs) await inbox.accept({ kind: 'message', msg: m })
      expect(inbox.unread).toBe(msgs.length)
      const got = inbox.take()
      expect(got.map((it) => (it.kind === 'message' ? it.msg.id : ''))).toEqual(
        msgs.map((m) => m.id),
      )
      expect(inbox.take()).toEqual([])
      expect(pushed).toEqual([])
      expect(delivered).toEqual(msgs.map((m) => [m.id, 'pull']))
    },
  )

  test.prop([fc.array(peer, { maxLength: 50 })])(
    'push mode: every message is pushed once, in order, and nothing is queued',
    async (froms) => {
      const { inbox, pushed, delivered } = harness(true)
      const msgs = froms.map((f, i) => message(i, f))
      for (const m of msgs) await inbox.accept({ kind: 'message', msg: m })
      expect(pushed.map((it) => (it.kind === 'message' ? it.msg.id : ''))).toEqual(
        msgs.map((m) => m.id),
      )
      expect(inbox.unread).toBe(0)
      expect(delivered).toEqual(msgs.map((m) => [m.id, 'push']))
    },
  )

  test.prop([fc.boolean(), peer, peer])(
    'a reply goes to the open ask only when it comes from the asked peer',
    async (push, asked, sender) => {
      const { inbox, pushed, delivered } = harness(push)
      const answer = inbox.expectReply('7', asked, 1000)
      const reply = message(99, sender, '7')
      await inbox.accept({ kind: 'message', msg: reply })
      if (sender === asked) {
        expect(await answer).toEqual(reply)
        expect(pushed).toEqual([])
        expect(inbox.unread).toBe(0)
        expect(delivered).toEqual([[reply.id, 'ask']])
      } else {
        expect(delivered).toEqual([[reply.id, push ? 'push' : 'pull']].slice(0, push ? 1 : 0))
        expect(pushed.length + inbox.unread).toBe(1)
      }
    },
  )

  test.prop([fc.array(peer, { minLength: 1, maxLength: 20 }), peer])(
    'wait(from) takes the first queued message from that peer and leaves the rest',
    async (froms, filter) => {
      const { inbox } = harness(false)
      const msgs = froms.map((f, i) => message(i, f))
      for (const m of msgs) await inbox.accept({ kind: 'message', msg: m })
      const first = msgs.find((m) => m.from === filter)
      const got = await inbox.wait(filter, 1)
      if (first === undefined) expect(got).toEqual([])
      else expect(got).toEqual([{ kind: 'message', msg: first }])
      expect(inbox.unread).toBe(msgs.length - (first === undefined ? 0 : 1))
    },
  )
})

const leftNotice = (peer: string): Item => ({
  kind: 'notice',
  notice: { kind: 'peer_left', peer, at: '2026-09-30T00:00:00.000Z' },
})

describe('a peer leaves', () => {
  test.prop([fc.boolean(), peer, peer])(
    'peer_left ends every ask and every wait on that peer, and goes nowhere else',
    async (push, waitedOn, left) => {
      const { inbox, pushed, delivered } = harness(push)
      const answer = inbox.expectReply('7', waitedOn, 30)
      const onPeer = inbox.wait(waitedOn, 30)
      const onAny = inbox.wait(undefined, 30)
      await inbox.accept(leftNotice(left))
      if (matchesPeer(waitedOn, left)) {
        expect(await answer).toBe('peer_left')
        expect(await onPeer).toEqual([leftNotice(left)])
      } else {
        expect(await answer).toBeUndefined()
        expect(await onPeer).toEqual([])
      }
      // A wait for any message is about messages; it runs on.
      expect(await onAny).toEqual([])
      expect(pushed).toEqual([])
      expect(inbox.unread).toBe(0)
      expect(delivered).toEqual([])
    },
  )
})

describe('peer filters', () => {
  test.prop([
    fc.stringMatching(/^[a-z]{1,5}$/),
    fc.stringMatching(/^[a-z]{1,5}$/),
    fc.stringMatching(/^[a-z]{1,5}$/),
  ])('a bare name matches every machine; a full name matches only itself', (a, m1, m2) => {
    expect(matchesPeer(a, `${a}@${m1}`)).toBe(true)
    expect(matchesPeer(`${a}@${m1}`, `${a}@${m2}`)).toBe(m1 === m2)
    expect(matchesPeer(a, undefined)).toBe(false)
  })
})
