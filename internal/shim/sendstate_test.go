package shim

import "testing"

// The hub tells the recipient's state with v0.3.0. The shim passes it through and refuses a
// value outside the known states.
func TestSendResponseState(t *testing.T) {
	base := `"id":"4","to":"bob@vps-2","online":true,"sent_at":"2026-10-02T10:00:00.000Z"`
	for _, c := range []struct {
		state string
		ok    bool
	}{{"", true}, {"working", true}, {"blocked", true}, {"away", true}, {"bogus", false}} {
		body := "{" + base
		if c.state != "" {
			body += `,"state":"` + c.state + `"`
		}
		body += "}"
		r, ok := decode[sendResponse]([]byte(body))
		if ok != c.ok || (ok && r.State != c.state) {
			t.Errorf("state %q: ok %v, got %+v", c.state, ok, r)
		}
	}
}
