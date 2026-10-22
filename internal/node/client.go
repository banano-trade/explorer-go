// Package node talks to a Banano node over RPC. Only read-only actions are used.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
	// slots limits concurrent calls so visitors can't pile work onto the node.
	slots chan struct{}
	wait  time.Duration
}

// New returns a client allowing up to 8 concurrent calls.
func New(url string) *Client { return NewLimited(url, 8, 3*time.Second) }

// NewLimited returns a client allowing n concurrent calls; a call that can't get
// a slot within wait fails with ErrBusy.
func NewLimited(url string, n int, wait time.Duration) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 60 * time.Second}, slots: make(chan struct{}, n), wait: wait}
}

// ErrBusy means every slot stayed taken; the caller should show "try again".
var ErrBusy = errors.New("node busy")

// readOnly is every RPC action the explorer may send. Anything else (wallet,
// send, control actions) is refused before it leaves the process.
var readOnly = map[string]bool{
	"block_count": true, "account_info": true, "account_history": true, "block_info": true, "blocks_info": true,
	"receivable": true, "receivable_exists": true, "delegators_count": true, "delegators": true,
	"representatives_online": true, "representatives": true, "confirmation_quorum": true, "confirmation_history": true,
	"available_supply": true, "telemetry": true, "peers": true, "version": true, "account_weight": true,
	"account_balance": true, "ledger": true,
}

// ErrNotFound is returned when the node does not know the account or block.
var ErrNotFound = errors.New("not found")

// Call posts an RPC action and decodes the JSON reply into out.
func (c *Client) Call(ctx context.Context, action string, params map[string]any, out any) error {
	if !readOnly[action] {
		return fmt.Errorf("node: action %q is not allowed", action)
	}
	if c.slots != nil {
		t := time.NewTimer(c.wait)
		select {
		case c.slots <- struct{}{}:
			t.Stop()
			defer func() { <-c.slots }()
		case <-t.C:
			return ErrBusy
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	body := map[string]any{"action": action}
	for k, v := range params {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("node %s: %w", action, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		switch e.Error {
		case "Account not found", "Block not found", "Bad account number", "Invalid block hash":
			return ErrNotFound
		}
		return fmt.Errorf("node %s: %s", action, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type BlockCount struct {
	Count     string `json:"count"`
	Unchecked string `json:"unchecked"`
	Cemented  string `json:"cemented"`
}

func (c *Client) BlockCount(ctx context.Context) (BlockCount, error) {
	var r BlockCount
	err := c.Call(ctx, "block_count", nil, &r)
	return r, err
}

type AccountInfo struct {
	Frontier           string `json:"frontier"`
	OpenBlock          string `json:"open_block"`
	Balance            string `json:"balance"`
	ModifiedTimestamp  string `json:"modified_timestamp"`
	BlockCount         string `json:"block_count"`
	ConfirmationHeight string `json:"confirmation_height"`
	Representative     string `json:"representative"`
	Weight             string `json:"weight"`
	Receivable         string `json:"receivable"`
	ConfirmedFrontier  string `json:"confirmed_frontier"`
	ConfirmedHeight    string `json:"confirmed_height"`
}

func (c *Client) AccountInfo(ctx context.Context, account string) (AccountInfo, error) {
	var r AccountInfo
	err := c.Call(ctx, "account_info", map[string]any{
		"account": account, "representative": "true", "weight": "true", "receivable": "true", "include_confirmed": "true",
	}, &r)
	return r, err
}

type HistoryEntry struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	Account        string `json:"account"`
	Amount         string `json:"amount"`
	LocalTimestamp string `json:"local_timestamp"`
	Height         string `json:"height"`
	Hash           string `json:"hash"`
	Confirmed      string `json:"confirmed"`
	Representative string `json:"representative"`
	Link           string `json:"link"`
	Balance        string `json:"balance"`
	Previous       string `json:"previous"`
}

type History struct {
	Account  string         `json:"account"` // the node ignores the requested account when head is set: check this
	History  []HistoryEntry `json:"history"`
	Previous string         `json:"previous"`
	Next     string         `json:"next"`
}

// AccountHistory returns count entries starting at head (empty = newest).
func (c *Client) AccountHistory(ctx context.Context, account string, count int, head string) (History, error) {
	p := map[string]any{"account": account, "count": fmt.Sprint(count), "raw": "true"}
	if head != "" {
		p["head"] = head
	}
	var r History
	err := c.Call(ctx, "account_history", p, &r)
	// raw=true puts send/receive in subtype; normalise to Type.
	for i := range r.History {
		h := &r.History[i]
		if h.Subtype != "" {
			h.Type = h.Subtype
		}
	}
	return r, err
}

type BlockContents struct {
	Type           string `json:"type"`
	Account        string `json:"account"`
	Previous       string `json:"previous"`
	Representative string `json:"representative"`
	Balance        string `json:"balance"`
	Link           string `json:"link"`
	LinkAsAccount  string `json:"link_as_account"`
	Signature      string `json:"signature"`
	Work           string `json:"work"`
}

type BlockInfo struct {
	BlockAccount   string          `json:"block_account"`
	Amount         string          `json:"amount"`
	Balance        string          `json:"balance"`
	Height         string          `json:"height"`
	LocalTimestamp string          `json:"local_timestamp"`
	Successor      string          `json:"successor"`
	Confirmed      string          `json:"confirmed"`
	Contents       BlockContents   `json:"contents"`
	Subtype        string          `json:"subtype"`
	SourceAccount  string          `json:"source_account"`
	Receivable     string          `json:"receivable"`
	ReceiveHash    string          `json:"receive_hash"`
	Raw            json.RawMessage `json:"-"`
}

func (c *Client) BlockInfo(ctx context.Context, hash string) (BlockInfo, error) {
	var raw json.RawMessage
	if err := c.Call(ctx, "block_info", map[string]any{"hash": hash, "json_block": "true", "include_linked_account": "true", "receivable": "true", "receive_hash": "true", "source": "true"}, &raw); err != nil {
		return BlockInfo{}, err
	}
	var r BlockInfo
	err := json.Unmarshal(raw, &r)
	r.Raw = raw
	return r, err
}

// BlocksInfo fetches many blocks at once; unknown hashes are skipped.
func (c *Client) BlocksInfo(ctx context.Context, hashes []string) (map[string]BlockInfo, error) {
	var r struct {
		Blocks map[string]BlockInfo `json:"blocks"`
	}
	if len(hashes) == 0 {
		return map[string]BlockInfo{}, nil
	}
	err := c.Call(ctx, "blocks_info", map[string]any{"hashes": hashes, "json_block": "true", "include_not_found": "true"}, &r)
	return r.Blocks, err
}

type ReceivableBlock struct {
	Amount string `json:"amount"`
	Source string `json:"source"`
}

// Receivable lists blocks waiting to be received, biggest first, above threshold raw.
func (c *Client) Receivable(ctx context.Context, account string, count int, threshold string) (map[string]ReceivableBlock, error) {
	var r struct {
		Blocks json.RawMessage `json:"blocks"`
	}
	p := map[string]any{"account": account, "count": fmt.Sprint(count), "source": "true", "sorting": "true"}
	if threshold != "" {
		p["threshold"] = threshold
	}
	if err := c.Call(ctx, "receivable", p, &r); err != nil {
		return nil, err
	}
	out := map[string]ReceivableBlock{}
	if len(r.Blocks) == 0 || r.Blocks[0] != '{' { // node returns "" when empty
		return out, nil
	}
	err := json.Unmarshal(r.Blocks, &out)
	return out, err
}

func (c *Client) DelegatorsCount(ctx context.Context, account string) (string, error) {
	var r struct {
		Count string `json:"count"`
	}
	err := c.Call(ctx, "delegators_count", map[string]any{"account": account}, &r)
	return r.Count, err
}

// Delegators returns up to count delegators with at least threshold raw.
func (c *Client) Delegators(ctx context.Context, account string, count int, threshold string) (map[string]string, error) {
	var r struct {
		Delegators json.RawMessage `json:"delegators"`
	}
	p := map[string]any{"account": account, "count": fmt.Sprint(count)}
	if threshold != "" {
		p["threshold"] = threshold
	}
	if err := c.Call(ctx, "delegators", p, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(r.Delegators) == 0 || r.Delegators[0] != '{' {
		return out, nil
	}
	err := json.Unmarshal(r.Delegators, &out)
	return out, err
}

// RepresentativesOnline returns online reps and their weight in raw.
func (c *Client) RepresentativesOnline(ctx context.Context) (map[string]string, error) {
	var r struct {
		Representatives map[string]struct {
			Weight string `json:"weight"`
		} `json:"representatives"`
	}
	if err := c.Call(ctx, "representatives_online", map[string]any{"weight": "true"}, &r); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(r.Representatives))
	for a, v := range r.Representatives {
		out[a] = v.Weight
	}
	return out, nil
}

// Representatives returns every rep with delegated weight in raw.
func (c *Client) Representatives(ctx context.Context) (map[string]string, error) {
	var r struct {
		Representatives map[string]string `json:"representatives"`
	}
	err := c.Call(ctx, "representatives", nil, &r)
	return r.Representatives, err
}

type Quorum struct {
	QuorumDelta           string `json:"quorum_delta"`
	OnlineWeightQuorumPct string `json:"online_weight_quorum_percent"`
	OnlineWeightMinimum   string `json:"online_weight_minimum"`
	OnlineStakeTotal      string `json:"online_stake_total"`
	PeersStakeTotal       string `json:"peers_stake_total"`
	Peers                 []struct {
		Account string `json:"account"`
		IP      string `json:"ip"`
		Weight  string `json:"weight"`
	} `json:"peers"`
}

func (c *Client) ConfirmationQuorum(ctx context.Context, peerDetails bool) (Quorum, error) {
	var r Quorum
	p := map[string]any{}
	if peerDetails {
		p["peer_details"] = "true"
	}
	err := c.Call(ctx, "confirmation_quorum", p, &r)
	return r, err
}

type Confirmation struct {
	Hash     string `json:"hash"`
	Duration string `json:"duration"`
	Time     string `json:"time"`
	Voters   string `json:"voters"`
}

type ConfirmationHistory struct {
	Stats struct {
		Count   string `json:"count"`
		Average string `json:"average"`
	} `json:"confirmation_stats"`
	Confirmations []Confirmation `json:"confirmations"`
}

func (c *Client) ConfirmationHistory(ctx context.Context) (ConfirmationHistory, error) {
	var r ConfirmationHistory
	err := c.Call(ctx, "confirmation_history", nil, &r)
	return r, err
}

func (c *Client) AvailableSupply(ctx context.Context) (string, error) {
	var r struct {
		Available string `json:"available"`
	}
	err := c.Call(ctx, "available_supply", nil, &r)
	return r.Available, err
}

type Telemetry struct {
	Address        string `json:"address"`
	Port           string `json:"port"`
	BlockCount     string `json:"block_count"`
	CementedCount  string `json:"cemented_count"`
	UncheckedCount string `json:"unchecked_count"`
	PeerCount      string `json:"peer_count"`
	MajorVersion   string `json:"major_version"`
	MinorVersion   string `json:"minor_version"`
	Uptime         string `json:"uptime"`
	NodeID         string `json:"node_id"`
	Maker          string `json:"maker"` // 0 = official node
}

func (c *Client) TelemetryRaw(ctx context.Context) ([]Telemetry, error) {
	var r struct {
		Metrics []Telemetry `json:"metrics"`
	}
	err := c.Call(ctx, "telemetry", map[string]any{"raw": "true"}, &r)
	return r.Metrics, err
}

func (c *Client) PeerCount(ctx context.Context) (int, error) {
	var r struct {
		Peers json.RawMessage `json:"peers"`
	}
	if err := c.Call(ctx, "peers", nil, &r); err != nil {
		return 0, err
	}
	var m map[string]any
	if json.Unmarshal(r.Peers, &m) == nil {
		return len(m), nil
	}
	return 0, nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var r struct {
		NodeVendor string `json:"node_vendor"`
	}
	err := c.Call(ctx, "version", nil, &r)
	return r.NodeVendor, err
}
