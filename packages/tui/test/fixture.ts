// A fixed session for snapshot and app tests: alice asks bob, bob answers, carol broadcasts,
// the operator steps in, a message is withdrawn, and one question stays open.
import { type Address, type BusEvent, type BusEventInput, buildPresenceKey } from '@coop/core'
import type { Update } from '../src/model.js'

export const FIX_SID = 'build-42'
export const NOW = Date.parse('2026-09-30T12:05:00.000Z')
const alice: Address = { agent: 'alice', machine: 'mac-1' }
const bob: Address = { agent: 'bob', machine: 'vps-2' }
const carol: Address = { agent: 'carol', machine: 'mac-3' }

export function fixture(): Update[] {
  const events: BusEvent[] = []
  let seq = 0
  const t = (s: number) => new Date(Date.parse('2026-09-30T12:00:00.000Z') + s * 1000).toISOString()
  const add = (e: BusEventInput) => {
    seq++
    const full: BusEvent = { ...e, seq }
    events.push(full)
    return String(seq)
  }
  const sid = FIX_SID
  const client = { name: 'claude-code', version: '2.1' }
  add({
    kind: 'evt',
    sid,
    from: alice,
    evt: { kind: 'joined', host: 'mac-1', cwd: '/src/app', client, at: t(0) },
  })
  add({
    kind: 'evt',
    sid,
    from: bob,
    evt: {
      kind: 'joined',
      host: 'vps-2',
      cwd: '/srv/api',
      client: { name: 'codex', version: '0.9' },
      at: t(2),
    },
  })
  add({
    kind: 'evt',
    sid,
    from: carol,
    evt: { kind: 'joined', host: 'mac-3', cwd: '/src/app', client, at: t(3) },
  })
  const plan = add({
    kind: 'msg',
    sid,
    from: carol,
    to: 'all',
    text: 'I take src/users.ts and the tests',
    sent_at: t(10),
  })
  add({
    kind: 'evt',
    sid,
    from: alice,
    evt: { kind: 'delivered', id: plan, via: 'push', at: t(10.12) },
  })
  add({ kind: 'evt', sid, from: bob, evt: { kind: 'delivered', id: plan, via: 'pull', at: t(14) } })
  const q = add({
    kind: 'msg',
    sid,
    from: alice,
    to: bob,
    text: 'What is the shape of GET /users?',
    sent_at: t(20),
  })
  add({
    kind: 'evt',
    sid,
    from: alice,
    evt: { kind: 'wait_start', from: 'bob@vps-2', reply_to: q, timeout_s: 300, at: t(20.05) },
  })
  add({ kind: 'evt', sid, from: bob, evt: { kind: 'delivered', id: q, via: 'pull', at: t(21) } })
  add({
    kind: 'evt',
    sid,
    from: bob,
    evt: { kind: 'state', state: 'working', note: 'answering alice', at: t(21.5) },
  })
  const a = add({
    kind: 'msg',
    sid,
    from: bob,
    to: alice,
    text: '{ id: number, name: string, email: string }',
    reply_to: q,
    sent_at: t(30),
  })
  add({
    kind: 'evt',
    sid,
    from: alice,
    evt: { kind: 'delivered', id: a, via: 'ask', at: t(30.09) },
  })
  add({ kind: 'evt', sid, from: alice, evt: { kind: 'wait_end', result: 'message', at: t(30.1) } })
  const wrong = add({
    kind: 'msg',
    sid,
    from: carol,
    to: bob,
    text: 'the password is hunter2',
    sent_at: t(40),
  })
  add({ kind: 'redact', sid, id: wrong, at: t(45) })
  add({
    kind: 'msg',
    sid,
    from: 'operator',
    to: 'all',
    text: 'Please run the tests before you say done',
    sent_at: t(50),
  })
  const q2 = add({
    kind: 'msg',
    sid,
    from: carol,
    to: bob,
    text: 'Can I change the users table?',
    sent_at: t(60),
  })
  add({
    kind: 'evt',
    sid,
    from: carol,
    evt: { kind: 'wait_start', from: 'bob@vps-2', reply_to: q2, timeout_s: 300, at: t(60.02) },
  })
  add({
    kind: 'evt',
    sid,
    from: bob,
    evt: { kind: 'state', state: 'blocked', note: 'waiting for CI', at: t(70) },
  })

  const presence = (
    a: Address,
    state: 'working' | 'blocked',
    note: string,
    waiting?: string,
  ): Update => ({
    kind: 'presence',
    key: buildPresenceKey({ sid, agent: a }),
    record: {
      host: a.machine,
      cwd: '/src/app',
      client,
      state,
      note,
      joined_at: t(0),
      queued: 0,
      ...(waiting === undefined ? {} : { waiting: { on: waiting, reply_to: q2, since: t(60.02) } }),
    },
  })
  return [
    { kind: 'session', sid, record: { status: 'open', created_at: t(-60) } },
    {
      kind: 'session',
      sid: 'docs',
      record: { status: 'closed', created_at: t(-600), closed_at: t(-60) },
    },
    ...events.map((e) => ({ kind: 'event' as const, e })),
    presence(alice, 'working', 'parser'),
    presence(bob, 'blocked', 'waiting for CI'),
    presence(carol, 'working', 'users.ts', 'bob@vps-2'),
  ]
}
