package gabp

import (
	"strings"
	"time"
)

const (
	AttentionCurrentMethod  = "attention/current"
	AttentionAckMethod      = "attention/ack"
	AttentionOpenedChannel  = "attention/opened"
	AttentionUpdatedChannel = "attention/updated"
	AttentionClearedChannel = "attention/cleared"
)

type AttentionSample struct {
	Level          string `json:"level"`
	Message        string `json:"message"`
	RepeatCount    int    `json:"repeatCount"`
	LatestSequence int64  `json:"latestSequence"`
}

type AttentionItem struct {
	AttentionID        string            `json:"attentionId"`
	State              string            `json:"state"`
	Severity           string            `json:"severity"`
	Blocking           bool              `json:"blocking"`
	StateInvalidated   bool              `json:"stateInvalidated"`
	Summary            string            `json:"summary"`
	CausalOperationID  string            `json:"causalOperationId,omitempty"`
	CausalMethod       string            `json:"causalMethod,omitempty"`
	OpenedAtSequence   int64             `json:"openedAtSequence"`
	LatestSequence     int64             `json:"latestSequence"`
	DiagnosticsCursor  *int64            `json:"diagnosticsCursor,omitempty"`
	TotalUrgentEntries int               `json:"totalUrgentEntries"`
	Sample             []AttentionSample `json:"sample,omitempty"`
}

type AttentionCurrentResult struct {
	Attention *AttentionItem `json:"attention"`
}

type AttentionAckParams struct {
	AttentionID string `json:"attentionId"`
}

type AttentionAckResult struct {
	Acknowledged     bool           `json:"acknowledged"`
	AttentionID      string         `json:"attentionId"`
	CurrentAttention *AttentionItem `json:"currentAttention"`
}

func (a *AttentionItem) Clone() *AttentionItem {
	if a == nil {
		return nil
	}

	cloned := *a
	if len(a.Sample) > 0 {
		cloned.Sample = make([]AttentionSample, len(a.Sample))
		copy(cloned.Sample, a.Sample)
	}
	if a.DiagnosticsCursor != nil {
		cursor := *a.DiagnosticsCursor
		cloned.DiagnosticsCursor = &cursor
	}

	return &cloned
}

func SupportsAttention(capabilities Capabilities) bool {
	return hasCapabilityEntry(capabilities.Methods, AttentionCurrentMethod) &&
		hasCapabilityEntry(capabilities.Methods, AttentionAckMethod)
}

func AttentionChannels(capabilities Capabilities) []string {
	channels := make([]string, 0, 3)
	for _, channel := range []string{AttentionOpenedChannel, AttentionUpdatedChannel, AttentionClearedChannel} {
		if hasCapabilityEntry(capabilities.Events, channel) {
			channels = append(channels, channel)
		}
	}
	return channels
}

func (c *Client) SupportsAttention() bool {
	return SupportsAttention(c.GetCapabilities())
}

func (c *Client) GetCurrentAttentionWithTimeout(timeout time.Duration) (*AttentionItem, error) {
	result, err := c.sendRequestWithTimeout(AttentionCurrentMethod, map[string]interface{}{}, timeout)
	if err != nil {
		return nil, err
	}

	var current AttentionCurrentResult
	if err := mapToStruct(result, &current); err != nil {
		return nil, err
	}

	return current.Attention, nil
}

func (c *Client) AcknowledgeAttentionWithTimeout(attentionID string, timeout time.Duration) (*AttentionAckResult, error) {
	result, err := c.sendRequestWithTimeout(AttentionAckMethod, AttentionAckParams{AttentionID: attentionID}, timeout)
	if err != nil {
		return nil, err
	}

	var ack AttentionAckResult
	if err := mapToStruct(result, &ack); err != nil {
		return nil, err
	}

	return &ack, nil
}

func hasCapabilityEntry(entries []string, target string) bool {
	for _, entry := range entries {
		if strings.EqualFold(entry, target) {
			return true
		}
	}
	return false
}
