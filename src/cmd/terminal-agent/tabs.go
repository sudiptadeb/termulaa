package main

import "time"

// LayoutNode describes a recursive split-pane layout tree.
// Leaf nodes have Type="pane" and a SessionID.
// Internal nodes have Type="split" with a Direction and two children.
type LayoutNode struct {
	Type      string      `json:"type"`                // "pane" or "split"
	SessionID string      `json:"session_id,omitempty"`
	Direction string      `json:"direction,omitempty"` // "v" (vertical) or "h" (horizontal)
	Ratio     float64     `json:"ratio,omitempty"`     // 0.0–1.0, split position
	First     *LayoutNode `json:"first,omitempty"`
	Second    *LayoutNode `json:"second,omitempty"`
}

// Tab represents a named terminal tab that contains a layout tree of panes.
type Tab struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Layout     *LayoutNode `json:"layout"`
	CreatedAt  time.Time   `json:"created_at"`
	LastActive time.Time   `json:"last_active"`
}

// TabInfo is a JSON-safe summary for API list responses.
type TabInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	PaneCount  int       `json:"pane_count"`
	Alive      bool      `json:"alive"`
	LastActive time.Time `json:"last_active"`
}

// NewTab creates a tab with a single-pane layout backed by the given session.
func NewTab(id, name, sessionID string) *Tab {
	now := time.Now()
	return &Tab{
		ID:   id,
		Name: name,
		Layout: &LayoutNode{
			Type:      "pane",
			SessionID: sessionID,
		},
		CreatedAt:  now,
		LastActive: now,
	}
}

// SessionIDs collects all session IDs referenced in the layout tree.
func (t *Tab) SessionIDs() []string {
	var ids []string
	walkLayout(t.Layout, func(node *LayoutNode) {
		if node.Type == "pane" && node.SessionID != "" {
			ids = append(ids, node.SessionID)
		}
	})
	return ids
}

// Info returns a TabInfo snapshot. It checks whether any pane's session is alive.
func (t *Tab) Info(sessions map[string]*Session) TabInfo {
	paneCount := 0
	alive := false

	walkLayout(t.Layout, func(node *LayoutNode) {
		if node.Type == "pane" {
			paneCount++
			if s, ok := sessions[node.SessionID]; ok && s.Alive {
				alive = true
			}
		}
	})

	return TabInfo{
		ID:         t.ID,
		Name:       t.Name,
		PaneCount:  paneCount,
		Alive:      alive,
		LastActive: t.LastActive,
	}
}

// walkLayout performs a depth-first traversal of the layout tree,
// calling fn on every node.
func walkLayout(node *LayoutNode, fn func(*LayoutNode)) {
	if node == nil {
		return
	}
	fn(node)
	walkLayout(node.First, fn)
	walkLayout(node.Second, fn)
}

// ---------------------------------------------------------------------------
// Layout tree manipulation (used by tab WS commands)
// ---------------------------------------------------------------------------

// splitLayoutNode finds the pane with targetSessionID and replaces it with a
// split node containing the original pane and a new pane with newSessionID.
// Returns the (possibly new) root. The original tree is mutated in place
// except when the root itself is the target pane.
func splitLayoutNode(node *LayoutNode, targetSessionID, newSessionID, direction string) *LayoutNode {
	if node == nil {
		return nil
	}

	if node.Type == "pane" && node.SessionID == targetSessionID {
		return &LayoutNode{
			Type:      "split",
			Direction: direction,
			Ratio:     0.5,
			First:     node,
			Second: &LayoutNode{
				Type:      "pane",
				SessionID: newSessionID,
			},
		}
	}

	if node.Type == "split" {
		node.First = splitLayoutNode(node.First, targetSessionID, newSessionID, direction)
		node.Second = splitLayoutNode(node.Second, targetSessionID, newSessionID, direction)
	}
	return node
}

// closePaneInLayout removes the pane with sessionID and promotes its sibling.
// Returns the (possibly new) root. If the target is not found, the tree is unchanged.
func closePaneInLayout(node *LayoutNode, sessionID string) *LayoutNode {
	if node == nil {
		return nil
	}

	// Can't close at the leaf level without knowing the parent.
	if node.Type == "pane" {
		return node
	}

	if node.Type == "split" {
		// Direct child is the target — promote sibling.
		if node.First != nil && node.First.Type == "pane" && node.First.SessionID == sessionID {
			return node.Second
		}
		if node.Second != nil && node.Second.Type == "pane" && node.Second.SessionID == sessionID {
			return node.First
		}

		// Recurse into children.
		node.First = closePaneInLayout(node.First, sessionID)
		node.Second = closePaneInLayout(node.Second, sessionID)
	}
	return node
}

// replaceAllSessions creates a deep copy of the layout tree with every pane's
// session ID replaced by a new generated ID. Returns the new tree and a map
// of oldSessionID → newSessionID.
func replaceAllSessions(node *LayoutNode) (*LayoutNode, map[string]string) {
	mapping := make(map[string]string)
	newNode := replaceAllSessionsRecursive(node, mapping)
	return newNode, mapping
}

func replaceAllSessionsRecursive(node *LayoutNode, mapping map[string]string) *LayoutNode {
	if node == nil {
		return nil
	}

	if node.Type == "pane" {
		newID := generateID()
		mapping[node.SessionID] = newID
		return &LayoutNode{
			Type:      "pane",
			SessionID: newID,
		}
	}

	if node.Type == "split" {
		return &LayoutNode{
			Type:      "split",
			Direction: node.Direction,
			Ratio:     node.Ratio,
			First:     replaceAllSessionsRecursive(node.First, mapping),
			Second:    replaceAllSessionsRecursive(node.Second, mapping),
		}
	}

	return nil
}
