package shim

// The HTTP contract between the hub and the shim, as packages/core/src/api.ts defines it.

// message is one message as the hub gives it to an agent. The SSE id of a message is its id.
type message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
}

// Notice kinds.
const (
	noticeKicked   = "kicked"
	noticeClosed   = "closed"
	noticeReopened = "reopened"
	noticeRedacted = "redacted"
	noticePeerLeft = "peer_left"
)

// notice is something the agent must know that is not a message.
type notice struct {
	Kind string `json:"kind"`
	// ID is the withdrawn message, for redacted.
	ID string `json:"id,omitempty"`
	// Peer is the peer that left, for peer_left.
	Peer string `json:"peer,omitempty"`
	At   string `json:"at"`
}
