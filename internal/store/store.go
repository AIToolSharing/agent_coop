// Package store keeps the hub's state in one SQLite file: the event log, the sessions, the
// kicks, the tokens, and what the hub knows about each agent. It replaces the NATS stream and
// the KV buckets of the TypeScript hub.
//
// The store has no clock: the caller passes every time stamp. One connection serves all
// callers, so SQLite never answers "busy" inside the hub; other processes (the admin
// commands) wait up to the busy timeout.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	_ "modernc.org/sqlite" // the database/sql driver "sqlite"
)

var (
	// ErrExists means the session exists.
	ErrExists = errors.New("session exists")
	// ErrNotFound means there is no such session.
	ErrNotFound = errors.New("no such session")
	// ErrOpen means the session is open; close it first.
	ErrOpen = errors.New("session is open")
)

// Store is one open database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
INSERT OR IGNORE INTO meta (key, value) VALUES ('revision', 0);
CREATE TABLE IF NOT EXISTS events (
	seq     INTEGER PRIMARY KEY AUTOINCREMENT,
	sid     TEXT NOT NULL,
	kind    TEXT NOT NULL,
	subject TEXT NOT NULL,
	payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS events_by_session ON events (sid, kind, seq);
CREATE TABLE IF NOT EXISTS sessions (
	sid        TEXT PRIMARY KEY,
	status     TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	closed_at  TEXT NOT NULL DEFAULT '',
	revision   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS kicks (
	sid      TEXT NOT NULL,
	target   TEXT NOT NULL,
	at       TEXT NOT NULL,
	revision INTEGER NOT NULL,
	PRIMARY KEY (sid, target)
);
CREATE TABLE IF NOT EXISTS tokens (
	name       TEXT PRIMARY KEY,
	sha256     TEXT NOT NULL,
	role       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	revoked_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS known (
	sid      TEXT NOT NULL,
	agent    TEXT NOT NULL,
	state    TEXT NOT NULL,
	note     TEXT NOT NULL DEFAULT '',
	seen_seq INTEGER NOT NULL,
	PRIMARY KEY (sid, agent)
);
`

// Open opens or creates the database at path, with its directory.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// WAL lets the admin commands read while the hub writes. An immediate transaction takes
	// the write lock at BEGIN, so a writer waits (busy timeout) in place of a late "busy".
	dsn := path + "?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// tx runs fn in one write transaction.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// bump gives the next revision. Every record write takes one, so the operator's feed can
// keep the newest value of a key, also across a restart of the hub.
func bump(tx *sql.Tx) (int64, error) {
	var rev int64
	err := tx.QueryRow(`UPDATE meta SET value = value + 1 WHERE key = 'revision' RETURNING value`).Scan(&rev)
	return rev, err
}

// NextRevision takes one revision for a record that lives outside the store (presence).
func (s *Store) NextRevision() (int64, error) {
	var rev int64
	err := s.tx(func(tx *sql.Tx) (err error) {
		rev, err = bump(tx)
		return err
	})
	return rev, err
}

// --- Events --------------------------------------------------------------------------------

// Known is the state and note of an agent, for Append.
type Known struct {
	State, Note string
}

// Append writes one event and gives its sequence, the message id. With seen, the sender has
// seen the log up to this event (a join, a delivery report, a leave). With k, the sender's
// state and note are set. Both go into the same transaction as the event.
func (s *Store) Append(e wire.Event, seen bool, k *Known) (int64, error) {
	subject, payload, err := wire.EncodeEvent(e)
	if err != nil {
		return 0, err
	}
	var seq int64
	err = s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO events (sid, kind, subject, payload) VALUES (?, ?, ?, ?)`, e.SID, e.Kind, subject, payload)
		if err != nil {
			return err
		}
		if seq, err = res.LastInsertId(); err != nil {
			return err
		}
		if !seen && k == nil {
			return nil
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO known (sid, agent, state, note, seen_seq) VALUES (?, ?, 'idle', '', 0)`, e.SID, e.From); err != nil {
			return err
		}
		if seen {
			if _, err := tx.Exec(`UPDATE known SET seen_seq = MAX(seen_seq, ?) WHERE sid = ? AND agent = ?`, seq, e.SID, e.From); err != nil {
				return err
			}
		}
		if k != nil {
			if _, err := tx.Exec(`UPDATE known SET state = ?, note = ? WHERE sid = ? AND agent = ?`, k.State, k.Note, e.SID, e.From); err != nil {
				return err
			}
		}
		return nil
	})
	return seq, err
}

// LastSeq gives the sequence of the newest event ever written, or 0.
func (s *Store) LastSeq() (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&seq)
	return seq, err
}

// Events gives the events of a session with a sequence in [from, to], oldest first. With
// kinds, only those kinds (msg, kick, redact, evt).
func (s *Store) Events(sid string, from, to int64, kinds ...string) ([]wire.Event, error) {
	q := `SELECT seq, subject, payload FROM events WHERE sid = ? AND seq BETWEEN ? AND ?`
	args := []any{sid, from, to}
	if len(kinds) > 0 {
		q += ` AND kind IN (?` + strings.Repeat(", ?", len(kinds)-1) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	rows, err := s.rows(q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]wire.Event, 0, len(rows))
	for _, r := range rows {
		if e, ok := wire.DecodeEvent(r.Subject, r.Payload, r.Seq); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// Row is one event in its wire form: the subject and the payload, as the operator's feed
// sends it.
type Row struct {
	Seq     int64
	Subject string
	Payload []byte
}

// Rows gives the events of every session with a sequence in [from, to], oldest first.
func (s *Store) Rows(from, to int64) ([]Row, error) {
	return s.rows(`SELECT seq, subject, payload FROM events WHERE seq BETWEEN ? AND ? ORDER BY seq`, from, to)
}

func (s *Store) rows(q string, args ...any) ([]Row, error) {
	rs, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.Seq, &r.Subject, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Delete erases one message of a session (a redact). It gives false when seq is not a
// message of that session.
func (s *Store) Delete(sid string, seq int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM events WHERE seq = ? AND sid = ? AND kind = ?`, seq, sid, wire.EventMsg)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// --- Sessions ------------------------------------------------------------------------------

// SessionRow is one session with the revision of its record.
type SessionRow struct {
	SID      string
	Record   wire.SessionRecord
	Revision int64
}

const sessionColumns = `sid, status, title, created_at, closed_at, revision`

func scanSession(sc interface{ Scan(...any) error }) (SessionRow, error) {
	var r SessionRow
	err := sc.Scan(&r.SID, &r.Record.Status, &r.Record.Title, &r.Record.CreatedAt, &r.Record.ClosedAt, &r.Revision)
	return r, err
}

// Session gives one session, or false.
func (s *Store) Session(sid string) (SessionRow, bool, error) {
	r, err := scanSession(s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE sid = ?`, sid))
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRow{}, false, nil
	}
	return r, err == nil, err
}

// Sessions gives every session, by id.
func (s *Store) Sessions() ([]SessionRow, error) {
	rs, err := s.db.Query(`SELECT ` + sessionColumns + ` FROM sessions ORDER BY sid`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []SessionRow
	for rs.Next() {
		r, err := scanSession(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// CreateSession makes an open session. ErrExists when there is one.
func (s *Store) CreateSession(sid, title, at string) (SessionRow, error) {
	var row SessionRow
	err := s.tx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE sid = ?`, sid).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExists
		}
		rev, err := bump(tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO sessions (sid, status, title, created_at, closed_at, revision) VALUES (?, 'open', ?, ?, '', ?)`, sid, title, at, rev)
		row = SessionRow{SID: sid, Record: wire.SessionRecord{Status: "open", Title: title, CreatedAt: at}, Revision: rev}
		return err
	})
	return row, err
}

// SetStatus opens or closes a session and gives the new record. changed is false when the
// session had that status already. A close records at as closed_at; an open clears it.
func (s *Store) SetStatus(sid, status, at string) (row SessionRow, changed bool, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		old, err := scanSession(tx.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE sid = ?`, sid))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		changed = old.Record.Status != status
		rev, err := bump(tx)
		if err != nil {
			return err
		}
		closedAt := ""
		if status == "closed" {
			closedAt = at
		}
		if _, err := tx.Exec(`UPDATE sessions SET status = ?, closed_at = ?, revision = ? WHERE sid = ?`, status, closedAt, rev, sid); err != nil {
			return err
		}
		row = old
		row.Record.Status, row.Record.ClosedAt, row.Revision = status, closedAt, rev
		return nil
	})
	return row, changed, err
}

// DeleteSession removes a closed session with its events, kicks and known agents. It gives
// the revision of the removal and the removed kicks, each with its own removal revision.
// ErrNotFound for an unknown session; ErrOpen for an open one.
func (s *Store) DeleteSession(sid string) (rev int64, kicks []KickRow, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRow(`SELECT status FROM sessions WHERE sid = ?`, sid).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "closed" {
			return ErrOpen
		}
		rs, err := tx.Query(`SELECT sid, target, at, revision FROM kicks WHERE sid = ? ORDER BY target`, sid)
		if err != nil {
			return err
		}
		kicks, err = scanKicks(rs)
		if err != nil {
			return err
		}
		for i := range kicks {
			if kicks[i].Revision, err = bump(tx); err != nil {
				return err
			}
		}
		if rev, err = bump(tx); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM events WHERE sid = ?`,
			`DELETE FROM known WHERE sid = ?`,
			`DELETE FROM kicks WHERE sid = ?`,
			`DELETE FROM sessions WHERE sid = ?`,
		} {
			if _, err := tx.Exec(q, sid); err != nil {
				return err
			}
		}
		return nil
	})
	return rev, kicks, err
}

// --- Kicks ---------------------------------------------------------------------------------

// KickRow is one removed agent of a session.
type KickRow struct {
	SID      string
	Target   string // agent@machine
	At       string
	Revision int64
}

func scanKicks(rs *sql.Rows) ([]KickRow, error) {
	defer rs.Close()
	var out []KickRow
	for rs.Next() {
		var k KickRow
		if err := rs.Scan(&k.SID, &k.Target, &k.At, &k.Revision); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rs.Err()
}

// Kick records that target may not join sid, and gives the record's revision.
func (s *Store) Kick(sid, target, at string) (int64, error) {
	var rev int64
	err := s.tx(func(tx *sql.Tx) (err error) {
		if rev, err = bump(tx); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO kicks (sid, target, at, revision) VALUES (?, ?, ?, ?)
			ON CONFLICT (sid, target) DO UPDATE SET at = excluded.at, revision = excluded.revision`, sid, target, at, rev)
		return err
	})
	return rev, err
}

// Unkick removes the kick record and gives the revision of the removal.
func (s *Store) Unkick(sid, target string) (int64, error) {
	var rev int64
	err := s.tx(func(tx *sql.Tx) (err error) {
		if rev, err = bump(tx); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM kicks WHERE sid = ? AND target = ?`, sid, target)
		return err
	})
	return rev, err
}

// Kicked reports whether target is removed from sid.
func (s *Store) Kicked(sid, target string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM kicks WHERE sid = ? AND target = ?`, sid, target).Scan(&n)
	return n > 0, err
}

// Kicks gives every kick record, by session and target.
func (s *Store) Kicks() ([]KickRow, error) {
	rs, err := s.db.Query(`SELECT sid, target, at, revision FROM kicks ORDER BY sid, target`)
	if err != nil {
		return nil, err
	}
	return scanKicks(rs)
}

// --- Known agents --------------------------------------------------------------------------

// KnownRow is an agent that was in a session: its last state and how far it saw the log.
type KnownRow struct {
	SID     string
	Agent   string // agent@machine
	State   string
	Note    string
	SeenSeq int64
}

// Known gives every agent that was in sid, by address.
func (s *Store) Known(sid string) ([]KnownRow, error) {
	rs, err := s.db.Query(`SELECT sid, agent, state, note, seen_seq FROM known WHERE sid = ? ORDER BY agent`, sid)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []KnownRow
	for rs.Next() {
		var k KnownRow
		if err := rs.Scan(&k.SID, &k.Agent, &k.State, &k.Note, &k.SeenSeq); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rs.Err()
}

// KnownAgent gives one known agent of sid, or false.
func (s *Store) KnownAgent(sid, agent string) (KnownRow, bool, error) {
	var k KnownRow
	err := s.db.QueryRow(`SELECT sid, agent, state, note, seen_seq FROM known WHERE sid = ? AND agent = ?`, sid, agent).
		Scan(&k.SID, &k.Agent, &k.State, &k.Note, &k.SeenSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return KnownRow{}, false, nil
	}
	return k, err == nil, err
}

// --- Tokens --------------------------------------------------------------------------------

// TokenRow is one token: its name, role and dates. The secret is never stored.
type TokenRow struct {
	Name      string
	Role      string // machine or operator
	CreatedAt string
	RevokedAt string // "" while valid
}

func sha(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// IssueToken makes a token `<name>.<secret>` with the role machine or operator. It replaces
// the old token of that name. Only the hash of the secret is stored.
func (s *Store) IssueToken(name, role, at string) (string, error) {
	if !wire.IsToken(name) {
		return "", fmt.Errorf("invalid token name: %s", name)
	}
	if role != "machine" && role != "operator" {
		return "", fmt.Errorf("invalid token role: %s", role)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw[:])
	_, err := s.db.Exec(`INSERT INTO tokens (name, sha256, role, created_at, revoked_at) VALUES (?, ?, ?, ?, '')
		ON CONFLICT (name) DO UPDATE SET sha256 = excluded.sha256, role = excluded.role, created_at = excluded.created_at, revoked_at = ''`,
		name, sha(secret), role, at)
	if err != nil {
		return "", err
	}
	return name + "." + secret, nil
}

// RevokeToken ends a token. It gives false when the name has no valid token.
func (s *Store) RevokeToken(name, at string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tokens SET revoked_at = ? WHERE name = ? AND revoked_at = ''`, at, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Tokens gives every token, by name.
func (s *Store) Tokens() ([]TokenRow, error) {
	rs, err := s.db.Query(`SELECT name, role, created_at, revoked_at FROM tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []TokenRow
	for rs.Next() {
		var t TokenRow
		if err := rs.Scan(&t.Name, &t.Role, &t.CreatedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rs.Err()
}

// VerifyToken gives the name and the role of a valid bearer token.
func (s *Store) VerifyToken(token string) (name, role string, ok bool, err error) {
	name, secret, found := strings.Cut(token, ".")
	if !found || !wire.IsToken(name) || secret == "" {
		return "", "", false, nil
	}
	var hash, revoked string
	err = s.db.QueryRow(`SELECT sha256, role, revoked_at FROM tokens WHERE name = ?`, name).Scan(&hash, &role, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if revoked != "" || subtle.ConstantTimeCompare([]byte(hash), []byte(sha(secret))) != 1 {
		return "", "", false, nil
	}
	return name, role, true, nil
}

// TokenValid reports whether name has a valid token now.
func (s *Store) TokenValid(name string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE name = ? AND revoked_at = ''`, name).Scan(&n)
	return n == 1, err
}
