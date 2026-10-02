package api

import (
	"errors"
	"testing"
)

// Found by Schemathesis (negative data): a JSON null in an optional field was taken as absent
// and accepted. zod's `.optional()` refuses null; so must the Go checks.
func TestNullInAnOptionalFieldIsInvalid(t *testing.T) {
	cases := []struct {
		name string
		body string
		call func([]byte) error
	}{
		{"create title", `{"session":"x","title":null}`, func(b []byte) error { _, _, err := parseCreateSession(b); return err }},
		{"send reply_to", `{"agent":"a","to":"all","text":"x","reply_to":null}`, func(b []byte) error { _, err := parseSend(b); return err }},
		{"operator send reply_to", `{"to":"all","text":"x","reply_to":null}`, func(b []byte) error { _, err := parseOperatorSend(b); return err }},
		{"state note", `{"kind":"state","agent":"a","state":"idle","note":null}`, func(b []byte) error { _, err := parseActivity(b); return err }},
		{"wait_start from", `{"kind":"wait_start","agent":"a","timeout_s":5,"from":null}`, func(b []byte) error { _, err := parseActivity(b); return err }},
		{"wait_start reply_to", `{"kind":"wait_start","agent":"a","timeout_s":5,"reply_to":null}`, func(b []byte) error { _, err := parseActivity(b); return err }},
	}
	for _, c := range cases {
		err := c.call([]byte(c.body))
		var inv invalid
		if !errors.As(err, &inv) {
			t.Errorf("%s: %s accepted (err %v), want invalid", c.name, c.body, err)
		}
	}
}

// The same bodies without the null field, and with a value in it, pass.
func TestOptionalFieldsAbsentOrSet(t *testing.T) {
	cases := []struct {
		name string
		body string
		call func([]byte) error
	}{
		{"create no title", `{"session":"x"}`, func(b []byte) error { _, _, err := parseCreateSession(b); return err }},
		{"create title", `{"session":"x","title":"T"}`, func(b []byte) error { _, _, err := parseCreateSession(b); return err }},
		{"send no reply_to", `{"agent":"a","to":"all","text":"x"}`, func(b []byte) error { _, err := parseSend(b); return err }},
		{"send reply_to", `{"agent":"a","to":"all","text":"x","reply_to":"7"}`, func(b []byte) error { _, err := parseSend(b); return err }},
		{"state note", `{"kind":"state","agent":"a","state":"idle","note":"n"}`, func(b []byte) error { _, err := parseActivity(b); return err }},
		{"state no note", `{"kind":"state","agent":"a","state":"idle"}`, func(b []byte) error { _, err := parseActivity(b); return err }},
		{"wait_start all", `{"kind":"wait_start","agent":"a","timeout_s":5,"from":"operator","reply_to":"1"}`, func(b []byte) error { _, err := parseActivity(b); return err }},
		{"wait_start none", `{"kind":"wait_start","agent":"a","timeout_s":5}`, func(b []byte) error { _, err := parseActivity(b); return err }},
	}
	for _, c := range cases {
		if err := c.call([]byte(c.body)); err != nil {
			t.Errorf("%s: %s refused: %v", c.name, c.body, err)
		}
	}
}

// A field of another activity kind is an unknown field, as in a zod discriminated union of
// strict objects.
func TestActivityRefusesFieldsOfAnotherKind(t *testing.T) {
	for _, body := range []string{
		`{"kind":"state","agent":"a","state":"idle","from":"operator"}`,
		`{"kind":"delivered","agent":"a","id":"1","via":"push"}`,
		`{"kind":"wait_end","agent":"a","result":"timeout","timeout_s":5}`,
		`{"kind":"wait_start","agent":"a","timeout_s":5,"result":"timeout"}`,
	} {
		if _, err := parseActivity([]byte(body)); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
}
