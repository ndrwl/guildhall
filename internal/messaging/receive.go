package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ndrwl/guildhall/internal/store"
)

// ReceiveSchema is the schema of what `messages receive` prints: the receiver's answer to one delivery.
const ReceiveSchema = "guildhall.receive-result/1"

// Receive statuses: delivered, stored with its receipt, or refused with the reason; a refusal is final and the sender
// never retries it. A receive that fails without either, such as a locked store, exits non-zero and prints nothing,
// and the sender retries it.
const (
	ReceiveDelivered = "delivered"
	ReceiveRefused   = "refused"
)

// ReceiveResult is what `messages receive` prints (docs/messaging.md#messages-receive).
type ReceiveResult struct {
	Schema  string         `json:"schema"`
	Status  string         `json:"status"`
	Receipt *store.Receipt `json:"receipt,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}

// ParseReceiveResult decodes a ReceiveResult strictly: a delivered one with its receipt, or a refused one with its
// reason.
func ParseReceiveResult(data []byte) (ReceiveResult, error) {
	var r ReceiveResult
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return ReceiveResult{}, fmt.Errorf("receive result: %w", err)
	}
	switch {
	case r.Schema != ReceiveSchema:
		return ReceiveResult{}, fmt.Errorf("receive result: schema %q, want %s", r.Schema, ReceiveSchema)
	case r.Status == ReceiveDelivered && r.Receipt != nil && r.Reason == "":
	case r.Status == ReceiveRefused && r.Receipt == nil && r.Reason != "":
	default:
		return ReceiveResult{}, fmt.Errorf("receive result: status %q needs a receipt when delivered and a reason when refused", r.Status)
	}
	return r, nil
}
