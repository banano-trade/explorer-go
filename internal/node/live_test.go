package node

import (
	"context"
	"os"
	"testing"
)

// Runs only with NODE_URL set, e.g. NODE_URL=http://[::1]:7072 go test ./internal/node/
func TestLive(t *testing.T) {
	url := os.Getenv("NODE_URL")
	if url == "" {
		t.Skip("NODE_URL not set")
	}
	c := New(url)
	ctx := context.Background()
	bc, err := c.BlockCount(ctx)
	if err != nil || bc.Count == "" {
		t.Fatalf("block_count: %v %+v", err, bc)
	}
	ai, err := c.AccountInfo(ctx, "ban_1jung1eb3uomk1gsx7w6w7toqrikxm5pgn5wbsg5fpy96ckpdf6wmiuuzpca")
	if err != nil || ai.BlockCount == "" {
		t.Fatalf("account_info: %v", err)
	}
	h, err := c.AccountHistory(ctx, "ban_1jung1eb3uomk1gsx7w6w7toqrikxm5pgn5wbsg5fpy96ckpdf6wmiuuzpca", 3, "")
	if err != nil || len(h.History) != 3 || h.History[0].Type == "" {
		t.Fatalf("history: %v %+v", err, h)
	}
	bi, err := c.BlockInfo(ctx, h.History[0].Hash)
	if err != nil || bi.BlockAccount == "" {
		t.Fatalf("block_info: %v", err)
	}
	if _, err := c.AccountInfo(ctx, "ban_1111111111111111111111111111111111111111111111111111hifc8npp"); err != nil && err != ErrNotFound {
		t.Fatalf("unopened: %v", err)
	}
	r, err := c.Receivable(ctx, "ban_1r5tszzun5q54i4aphwpth99oy3t8mkkr7amkit1ra4u7s9tq85zee6j8s4s", 5, "")
	if err != nil || len(r) == 0 {
		t.Fatalf("receivable: %v %d", err, len(r))
	}
	ch, err := c.ConfirmationHistory(ctx)
	if err != nil {
		t.Fatalf("confirmation_history: %v", err)
	}
	t.Logf("blocks=%s history0=%s recv=%d conf_avg=%s", bc.Count, h.History[0].Type, len(r), ch.Stats.Average)
}
