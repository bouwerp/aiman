package fakeagent

// Todo is one item in the agent's own task list snapshot.
type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// Step is one scripted fake-agent action.
type Step struct {
	AfterMS int    `json:"after_ms"`
	Event   string `json:"event"` // SessionStart | Stop | Notification | PreCompact | SessionEnd
	State   string `json:"state"` // idle | working | waiting_input | ended
	Message string `json:"message,omitempty"`
	Todo    []Todo `json:"todo,omitempty"` // mirror payload
	Exit    bool   `json:"exit,omitempty"`
}

// Script is the JSON a fake-agent run reads.
type Script struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Steps     []Step `json:"steps"`
}
