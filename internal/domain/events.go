package domain

// Webhook event types emitted through the transactional outbox.
const (
	EventTransactionPosted   = "transaction.posted"
	EventTransactionPending  = "transaction.pending"
	EventTransactionVoided   = "transaction.voided"
	EventTransactionReversed = "transaction.reversed"
)

// EventTypes lists every event type a webhook may subscribe to.
var EventTypes = []string{
	EventTransactionPosted,
	EventTransactionPending,
	EventTransactionVoided,
	EventTransactionReversed,
}
