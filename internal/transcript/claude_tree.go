package transcript

// claudeNode is one line with a uuid in the conversation tree.
type claudeNode struct {
	parent   *claudeNode
	children []*claudeNode
	items    []string
	turn     bool // holds a prompt or a task notification
}

// link adds the line to the tree. A compact boundary has a null parent but
// starts no new conversation, so it stays off the root: nothing after it can
// fork from before it. Lines kept across a compaction keep their old parents,
// and a prompt following one of them is therefore not a fork. A parent not
// seen (or dropped by a rewind) leaves the line detached in the same way.
func (c *claude) link(e *claudeEvent) {
	n := &claudeNode{turn: e.opensTurn() || e.isNotification()}
	if parent := c.parentOf(e); parent != nil {
		n.parent = parent
		parent.children = append(parent.children, n)
	}
	c.nodes[e.UUID] = n
	e.node = n
}

func (c *claude) parentOf(e *claudeEvent) *claudeNode {
	if e.Subtype == claudeCompactBoundary {
		return nil
	}
	if e.ParentUUID == "" {
		return c.root
	}
	return c.nodes[e.ParentUUID]
}

// rewind removes the branch a new prompt abandons. The tree forks for other
// reasons: parallel tool calls chain their assistant lines while each result
// hangs off its own call, and attachments sit beside results. Those branches
// hold no prompt. A rewind always replaces a prompt, so the abandoned side
// must contain one.
func (c *claude) rewind(e *claudeEvent) {
	path, fork := c.forkFromTip(c.parentOf(e))
	if fork == nil {
		return
	}
	abandoned := abandonedNodes(fork, path)
	holdsTurn := false
	for _, n := range abandoned {
		holdsTurn = holdsTurn || n.turn
	}
	if !holdsTurn {
		return
	}
	c.drop(e, fork, path, abandoned)
}

// forkFromTip walks up from the new line's parent. When the tip is on that
// walk there is no fork. Otherwise fork is where the tip's chain joins it, and
// nil when the two share nothing.
func (c *claude) forkFromTip(parent *claudeNode) (path map[*claudeNode]bool, fork *claudeNode) {
	if c.tip == nil || parent == nil {
		return nil, nil
	}
	path = map[*claudeNode]bool{}
	for n := parent; n != nil; n = n.parent {
		if n == c.tip {
			return nil, nil
		}
		path[n] = true
	}
	for n := c.tip; n != nil; n = n.parent {
		if path[n] {
			return path, n
		}
	}
	return nil, nil
}

// abandonedNodes lists fork's descendants off path, parents first, in the
// order the lines arrived.
func abandonedNodes(fork *claudeNode, path map[*claudeNode]bool) []*claudeNode {
	var out []*claudeNode
	var walk func(n *claudeNode)
	walk = func(n *claudeNode) {
		for _, child := range n.children {
			if !path[child] {
				out = append(out, child)
				walk(child)
			}
		}
	}
	walk(fork)
	return out
}

// drop emits a Remove per abandoned item and forgets the branch.
func (c *claude) drop(e *claudeEvent, fork *claudeNode, path map[*claudeNode]bool, abandoned []*claudeNode) {
	gone := make(map[*claudeNode]bool, len(abandoned))
	removed := map[string]bool{}
	for _, n := range abandoned {
		gone[n] = true
		for _, id := range n.items {
			removed[id] = true
			e.deltas = append(e.deltas, Delta{Remove: id})
		}
	}
	for uuid, n := range c.nodes {
		if gone[n] {
			delete(c.nodes, uuid)
		}
	}
	var kept []*claudeNode
	for _, child := range fork.children {
		if path[child] {
			kept = append(kept, child)
		}
	}
	fork.children = kept
	c.forgetItems(removed)
}

// forgetItems drops the lookups that point at removed items, so a late result
// or notification for the abandoned branch changes nothing.
func (c *claude) forgetItems(removed map[string]bool) {
	for id, it := range c.calls {
		if removed[it.ID] {
			delete(c.calls, id)
		}
	}
	for id, it := range c.subagents {
		if removed[it.ID] {
			delete(c.subagents, id)
			delete(c.outputs, id)
		}
	}
	for id, it := range c.processes {
		if removed[it.ID] {
			delete(c.processes, id)
			delete(c.outputs, id)
		}
	}
}
