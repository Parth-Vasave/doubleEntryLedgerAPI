// Package webhook delivers outbox events to subscriber endpoints with
// HMAC-SHA256 signatures, retries and exponential backoff (at-least-once).
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/observability"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// Header names sent with every delivery.
const (
	SignatureHeader = "Ledger-Signature"
	EventIDHeader   = "Ledger-Event-Id"
	EventTypeHeader = "Ledger-Event-Type"
)

// NewSecret returns a random signing secret.
func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(b[:]), nil
}

// Sign returns the signature header value for body at time ts:
// "t=<unix>,v1=<hex(HMAC-SHA256(secret, "<unix>." + body))>". Including the
// timestamp in the MAC lets receivers reject replays of old deliveries.
func Sign(secret string, ts time.Time, body []byte) string {
	unix := strconv.FormatInt(ts.Unix(), 10)
	return "t=" + unix + ",v1=" + mac(secret, unix, body)
}

func mac(secret, unix string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(unix))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature header and that its timestamp is within tolerance.
// Receivers can use this (or an equivalent) to authenticate deliveries.
func Verify(secret, header string, body []byte, tolerance time.Duration, now time.Time) error {
	var unix, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			unix = v
		case "v1":
			sig = v
		}
	}
	ts, err := strconv.ParseInt(unix, 10, 64)
	if err != nil || sig == "" {
		return fmt.Errorf("webhook: malformed signature header")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("webhook: timestamp outside tolerance")
	}
	if !hmac.Equal([]byte(sig), []byte(mac(secret, unix, body))) {
		return fmt.Errorf("webhook: signature mismatch")
	}
	return nil
}

// Envelope is the JSON body POSTed to subscribers.
type Envelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Attempt   int             `json:"attempt"`
	Data      json.RawMessage `json:"data"`
}

// Dispatcher relays the outbox and delivers webhooks.
type Dispatcher struct {
	Pool        *pgxpool.Pool
	Client      *http.Client
	Log         *slog.Logger
	MaxAttempts int
	BatchSize   int
	Concurrency int
	// Backoff returns the delay before retry n (n >= 1). Defaults to
	// exponential backoff with full jitter, capped at one hour.
	Backoff func(attempt int) time.Duration
}

// RelayOnce fans unpublished outbox events out to webhook deliveries.
func (d *Dispatcher) RelayOnce(ctx context.Context) (int64, error) {
	n, err := store.RelayOutbox(ctx, d.Pool, d.batch())
	if n > 0 {
		observability.OutboxRelayed.Add(float64(n))
	}
	return n, err
}

// DeliverOnce leases due deliveries and attempts them concurrently.
// It returns how many deliveries were attempted.
func (d *Dispatcher) DeliverOnce(ctx context.Context) (int, error) {
	timeout := d.Client.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ds, err := store.LeaseDeliveries(ctx, d.Pool, d.batch(), 2*timeout)
	if err != nil || len(ds) == 0 {
		return 0, err
	}
	sem := make(chan struct{}, max(d.Concurrency, 1))
	var wg sync.WaitGroup
	for _, del := range ds {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			res := d.attempt(ctx, del)
			if err := store.CompleteDelivery(ctx, d.Pool, res); err != nil {
				d.Log.ErrorContext(ctx, "record webhook delivery", "error", err, "delivery_id", del.ID)
			}
		}()
	}
	wg.Wait()
	return len(ds), nil
}

func (d *Dispatcher) attempt(ctx context.Context, del store.Delivery) store.DeliveryResult {
	res := store.DeliveryResult{ID: del.ID}
	body, err := json.Marshal(Envelope{
		ID: del.EventID.String(), Type: del.Type, CreatedAt: del.CreatedAt, Attempt: del.Attempt, Data: del.Payload,
	})
	if err == nil {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, del.URL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "ledger-webhooks/1")
			req.Header.Set(EventIDHeader, del.EventID.String())
			req.Header.Set(EventTypeHeader, del.Type)
			req.Header.Set(SignatureHeader, Sign(del.Secret, time.Now(), body))
			var resp *http.Response
			resp, err = d.Client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				_ = resp.Body.Close()
				res.StatusCode = resp.StatusCode
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					res.Delivered = true
					observability.WebhookDeliveries.WithLabelValues("delivered").Inc()
					return res
				}
				err = fmt.Errorf("endpoint returned %d", resp.StatusCode)
			}
		}
	}
	res.Error = truncate(err.Error(), 500)
	if del.Attempt >= d.MaxAttempts {
		res.Failed = true
		observability.WebhookDeliveries.WithLabelValues("failed").Inc()
		d.Log.WarnContext(ctx, "webhook delivery failed permanently",
			"delivery_id", del.ID, "webhook_id", del.WebhookID, "attempts", del.Attempt, "error", res.Error)
		return res
	}
	res.RetryIn = d.backoff(del.Attempt)
	observability.WebhookDeliveries.WithLabelValues("retry").Inc()
	return res
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	if d.Backoff != nil {
		return d.Backoff(attempt)
	}
	return ExponentialBackoff(attempt)
}

// ExponentialBackoff is 2^attempt seconds with full jitter, capped at 1 hour.
func ExponentialBackoff(attempt int) time.Duration {
	ceiling := math.Min(math.Pow(2, float64(attempt)), 3600)
	return time.Duration(mrand.Float64() * ceiling * float64(time.Second)) //nolint:gosec // jitter needs no CSPRNG
}

func (d *Dispatcher) batch() int {
	if d.BatchSize <= 0 {
		return 100
	}
	return d.BatchSize
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
