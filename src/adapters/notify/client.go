// Package notify is the notification adapter: it sends one message to one URL.
// The URL is a secret, so no error from here carries it.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

const signatureHeader = "X-1keyes-Signature"

// Config configures the adapter. HTTPClient, when set, is copied: the adapter
// installs its own redirect policy on the copy.
type Config struct {
	URL        string
	Secret     string // when set, each message is signed with it
	HTTPClient *http.Client
}

// Client implements ports.Notifier.
type Client struct {
	url, secret string
	hc          *http.Client
}

var _ ports.Notifier = (*Client)(nil)

// New builds the adapter. A redirect is never followed.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("notify adapter: a URL is required")
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		hc = &copied
		if hc.Timeout == 0 {
			hc.Timeout = 30 * time.Second
		}
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{url: cfg.URL, secret: cfg.Secret, hc: hc}, nil
}

// Notify posts the body once. Only a 2xx answer is a delivery.
func (c *Client) Notify(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("notify adapter: the request could not be made")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		mac := hmac.New(sha256.New, []byte(c.secret))
		mac.Write(body)
		req.Header[signatureHeader] = []string{"sha256=" + hex.EncodeToString(mac.Sum(nil))}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// The cause can hold the URL, so it is left out.
		return errors.New("notify adapter: no response from the notification endpoint")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("notify adapter: the notification endpoint answered %d", resp.StatusCode)
	}
	return nil
}
