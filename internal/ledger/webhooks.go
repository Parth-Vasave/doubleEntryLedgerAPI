package ledger

import (
	"context"
	"net/url"
	"slices"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/webhook"
)

// CreateWebhookRequest subscribes a URL to ledger events.
type CreateWebhookRequest struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
}

// CreateWebhook registers a webhook and returns it with its signing secret.
func (s *Service) CreateWebhook(ctx context.Context, ledgerID uuid.UUID, req CreateWebhookRequest) (*store.Webhook, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(req.URL) > 2048 {
		return nil, domain.Invalid("url must be an absolute http(s) URL")
	}
	if len(req.EventTypes) == 0 {
		req.EventTypes = domain.EventTypes
	}
	for _, t := range req.EventTypes {
		if !slices.Contains(domain.EventTypes, t) {
			return nil, domain.Invalid("unknown event type %q; valid types: %v", t, domain.EventTypes)
		}
	}
	if _, err := s.GetLedger(ctx, ledgerID); err != nil {
		return nil, err
	}
	secret, err := webhook.NewSecret()
	if err != nil {
		return nil, err
	}
	w := &store.Webhook{ID: newID(), LedgerID: ledgerID, URL: req.URL, EventTypes: req.EventTypes, Secret: secret}
	if err := store.InsertWebhook(ctx, s.pool, w); err != nil {
		return nil, err
	}
	return w, nil
}

// ListWebhooks lists a ledger's webhooks.
func (s *Service) ListWebhooks(ctx context.Context, ledgerID uuid.UUID) ([]store.Webhook, error) {
	return store.ListWebhooks(ctx, s.pool, ledgerID)
}

// DeleteWebhook deactivates a webhook.
func (s *Service) DeleteWebhook(ctx context.Context, ledgerID, id uuid.UUID) error {
	return store.DeactivateWebhook(ctx, s.pool, ledgerID, id)
}
