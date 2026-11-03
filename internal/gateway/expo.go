package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	expoSendURL     = "https://exp.host/--/api/v2/push/send"
	expoReceiptsURL = "https://exp.host/--/api/v2/push/getReceipts"
	// ReceiptInterval is how often the gateway asks Expo for receipts.
	ReceiptInterval = 15 * time.Minute
	// receiptAge is how old a ticket must be before its receipt is asked for;
	// Expo needs time to hand the message to APNs.
	receiptAge = 15 * time.Minute
	// ticketMaxAge drops a ticket Expo never answered for; Expo keeps
	// receipts for a day.
	ticketMaxAge = 24 * time.Hour
	// Expo accepts at most 100 messages per send and 1000 ids per receipt
	// request.
	expoSendBatch    = 100
	expoReceiptBatch = 1000
	expoBodyLimit    = 1 << 20

	expoDeviceNotRegistered = "DeviceNotRegistered"
)

type expoTicket struct {
	token  string
	sentAt time.Time
}

// ExpoClient sends pushes through Expo and prunes tokens Expo reports as
// no longer registered, at send time and from receipts.
type ExpoClient struct {
	SendURL     string
	ReceiptsURL string
	HTTP        *http.Client

	store *PushStore
	now   func() time.Time

	mu      sync.Mutex
	tickets map[string]expoTicket
}

// NewExpoClient returns a client against Expo's public endpoints.
func NewExpoClient(store *PushStore) *ExpoClient {
	return &ExpoClient{
		SendURL:     expoSendURL,
		ReceiptsURL: expoReceiptsURL,
		HTTP:        &http.Client{Timeout: 10 * time.Second},
		store:       store,
		now:         time.Now,
		tickets:     map[string]expoTicket{},
	}
}

type expoDetails struct {
	Error string `json:"error"`
}

type expoTicketReply struct {
	Status  string      `json:"status"`
	ID      string      `json:"id"`
	Details expoDetails `json:"details"`
}

type expoRequestError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Send posts msgs in batches. A ticket saying DeviceNotRegistered drops its
// token at once; ok tickets are kept for the receipt check.
func (c *ExpoClient) Send(ctx context.Context, msgs []expoMessage) error {
	for batch := range slices.Chunk(msgs, expoSendBatch) {
		if err := c.sendBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

func (c *ExpoClient) sendBatch(ctx context.Context, msgs []expoMessage) error {
	var reply struct {
		Data   []expoTicketReply  `json:"data"`
		Errors []expoRequestError `json:"errors"`
	}
	if err := c.post(ctx, c.SendURL, msgs, &reply); err != nil {
		return err
	}
	if len(reply.Errors) > 0 {
		return fmt.Errorf("expo push: %s", reply.Errors[0].Code)
	}
	sentAt := c.now()
	for i, t := range reply.Data {
		if i >= len(msgs) {
			break
		}
		switch {
		case t.Status == "ok" && t.ID != "":
			c.mu.Lock()
			c.tickets[t.ID] = expoTicket{token: msgs[i].To, sentAt: sentAt}
			c.mu.Unlock()
		case t.Details.Error == expoDeviceNotRegistered:
			c.dropToken(msgs[i].To)
		}
	}
	return nil
}

// CheckReceipts asks Expo about tickets older than receiptAge. Answered
// tickets are forgotten; DeviceNotRegistered drops the token; tickets older
// than ticketMaxAge are forgotten unasked.
func (c *ExpoClient) CheckReceipts(ctx context.Context) error {
	due := c.dueTickets()
	for batch := range slices.Chunk(due, expoReceiptBatch) {
		var reply struct {
			Data map[string]expoTicketReply `json:"data"`
		}
		if err := c.post(ctx, c.ReceiptsURL, map[string][]string{"ids": batch}, &reply); err != nil {
			return err
		}
		for id, rc := range reply.Data {
			c.mu.Lock()
			t, ok := c.tickets[id]
			delete(c.tickets, id)
			c.mu.Unlock()
			if ok && rc.Details.Error == expoDeviceNotRegistered {
				c.dropToken(t.token)
			}
		}
	}
	return nil
}

// RunReceipts checks receipts every interval until ctx ends.
func (c *ExpoClient) RunReceipts(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.CheckReceipts(ctx); err != nil {
				log.Printf("expo receipts: %v", err)
			}
		}
	}
}

func (c *ExpoClient) dueTickets() []string {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var due []string
	for id, t := range c.tickets {
		age := now.Sub(t.sentAt)
		switch {
		case age >= ticketMaxAge:
			delete(c.tickets, id)
		case age >= receiptAge:
			due = append(due, id)
		}
	}
	slices.Sort(due)
	return due
}

func (c *ExpoClient) ticketIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for id := range c.tickets {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (c *ExpoClient) dropToken(token string) {
	if c.store == nil || token == "" {
		return
	}
	if err := c.store.Unregister(token, ""); err != nil {
		log.Printf("expo: dropping an unregistered device: %v", err)
	}
}

// post sends body as JSON and decodes a 2xx reply into out. The error never
// carries the request body, which holds push tokens.
func (c *ExpoClient) post(ctx context.Context, url string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("expo push: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, expoBodyLimit))
	if err != nil {
		return fmt.Errorf("expo push: reading reply: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("expo push: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("expo push: decoding reply: %w", err)
	}
	return nil
}
