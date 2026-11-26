package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
)

// drafts.create is not in Slack's published method list, but it accepts a
// user token. drafts.list, drafts.update and drafts.delete answer
// not_allowed_token_type to the same token, so aiman can create a draft and
// cannot read, change or remove one.

// DraftRequest is a drafts.create request: one attached draft in Channel,
// optionally a reply in Thread (Broadcast also shows it in the channel).
type DraftRequest struct {
	Channel   string
	Text      string
	Thread    string
	Broadcast bool
}

// Draft is the draft Slack saved.
type Draft struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Thread  string `json:"thread_ts,omitempty"`
}

// draftHints explain the drafts.create refusals a caller can act on.
var draftHints = map[string]string{
	"attached_draft_exists":  "this channel already has a draft; send or delete it in Slack first (aiman cannot change or delete drafts)",
	"not_in_channel":         "the approving member is not in this channel",
	"invalid_channel":        "no channel with that id is visible to the approving member",
	"not_allowed_token_type": "Slack no longer accepts a user token for drafts.create",
}

// Draft saves Text as a draft in the approving member's Drafts & Sent
// without sending it. The text goes in as plain rich text: drafts.create
// takes blocks, not markdown.
func (c *Client) Draft(ctx context.Context, r DraftRequest) (Draft, error) {
	dest := map[string]any{"channel_id": r.Channel}
	if r.Thread != "" {
		dest["thread_ts"] = r.Thread
		if r.Broadcast {
			dest["broadcast"] = true
		}
	}
	destinations, err := json.Marshal([]any{dest})
	if err != nil {
		return Draft{}, err
	}
	blocks, err := json.Marshal([]any{map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":     "rich_text_section",
			"elements": []any{map[string]any{"type": "text", "text": r.Text}},
		}},
	}})
	if err != nil {
		return Draft{}, err
	}
	form := url.Values{
		"destinations":     {string(destinations)},
		"blocks":           {string(blocks)},
		"client_msg_id":    {uuid.NewString()},
		"file_ids":         {"[]"},
		"is_from_composer": {"false"},
	}
	var out struct {
		Draft struct {
			ID string `json:"id"`
		} `json:"draft"`
	}
	if err := c.callForm(ctx, "drafts.create", form, &out); err != nil {
		var fail *apiFailure
		if errors.As(err, &fail) {
			if hint, ok := draftHints[fail.Code]; ok {
				return Draft{}, fmt.Errorf("%w: %s", err, hint)
			}
		}
		return Draft{}, err
	}
	return Draft{ID: out.Draft.ID, Channel: r.Channel, Thread: r.Thread}, nil
}
