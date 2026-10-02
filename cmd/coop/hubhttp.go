package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/pin"
)

// hubHTTP gives the HTTP client for the hub in cfg: with the pinned certificate when there is
// one. A pin that is not a fingerprint is reported and ignored.
func hubHTTP(cfg config.Config, stderr io.Writer) *http.Client {
	c, err := pin.Client(cfg.CertSHA256)
	if err != nil {
		fmt.Fprintln(stderr, "ignoring COOP_CERT_SHA256:", err)
		return &http.Client{}
	}
	return c
}
