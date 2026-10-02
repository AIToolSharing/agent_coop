// Package pin trusts one self-signed hub certificate by its fingerprint, the way SSH trusts a
// host key. `coop login` shows the fingerprint once and stores it; every later connection
// checks the hub's certificate against it. A hub with a certificate from a public authority
// needs no pin: the default verification applies.
package pin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Format shows a fingerprint as openssl does: SHA256:AB:CD:...
func Format(pin string) string {
	up := strings.ToUpper(pin)
	parts := make([]string, 0, len(up)/2)
	for i := 0; i+2 <= len(up); i += 2 {
		parts = append(parts, up[i:i+2])
	}
	return "SHA256:" + strings.Join(parts, ":")
}

func decode(pin string) ([]byte, error) {
	raw, err := hex.DecodeString(pin)
	if err != nil || len(raw) != sha256.Size {
		return nil, fmt.Errorf("pin: %q is not a SHA-256 fingerprint in hex", pin)
	}
	return raw, nil
}

// Client gives an HTTP client. With an empty pin it verifies certificates as usual. With a pin
// it accepts only the certificate with that fingerprint, whoever signed it and whatever name
// it carries; plain http is not affected.
func Client(pin string) (*http.Client, error) {
	if pin == "" {
		return &http.Client{}, nil
	}
	want, err := decode(pin)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		// VerifyConnection below does the check; the usual chain and name checks are off on
		// purpose, because the certificate is self-signed.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return &tls.CertificateVerificationError{Err: errors.New("the hub sent no certificate")}
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], want) {
				return &tls.CertificateVerificationError{Err: fmt.Errorf("the hub's certificate (%s) is not the pinned one; run coop login again", Format(hex.EncodeToString(sum[:])))}
			}
			return nil
		},
	}
	return &http.Client{Transport: tr}, nil
}

// Fingerprint connects to an https address and gives the SHA-256 of the certificate it
// presents, in hex. It does not check the certificate: the caller shows the fingerprint and
// decides whether to trust it.
func Fingerprint(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("pin: %s is not an https address", rawURL)
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", rawURL, err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("pin: the hub sent no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// IsCertError reports whether err comes from a certificate the client did not accept.
func IsCertError(err error) bool {
	if err == nil {
		return false
	}
	var cve *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	return errors.As(err, &cve) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci)
}
