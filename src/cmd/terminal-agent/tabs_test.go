package main

import "testing"

func TestLayoutMatchesSessionSet(t *testing.T) {
	layout := &LayoutNode{
		Type:      "split",
		Direction: "vertical",
		First:     &LayoutNode{Type: "pane", SessionID: "a"},
		Second:    &LayoutNode{Type: "pane", SessionID: "b"},
	}

	if !layoutMatchesSessionSet(layout, []string{"a", "b"}) {
		t.Fatal("expected layout to match the session set")
	}
	if layoutMatchesSessionSet(layout, []string{"a"}) {
		t.Fatal("expected layout with an extra pane to be rejected")
	}

	duplicate := &LayoutNode{
		Type:      "split",
		Direction: "vertical",
		First:     &LayoutNode{Type: "pane", SessionID: "a"},
		Second:    &LayoutNode{Type: "pane", SessionID: "a"},
	}
	if layoutMatchesSessionSet(duplicate, []string{"a", "b"}) {
		t.Fatal("expected duplicate session IDs to be rejected")
	}
}

func TestSplitLayoutNodeReportsTargetFound(t *testing.T) {
	layout := &LayoutNode{Type: "pane", SessionID: "root"}

	split, found := splitLayoutNode(layout, "root", "new", "vertical")
	if !found {
		t.Fatal("expected split target to be found")
	}
	if split.Type != "split" {
		t.Fatalf("expected split node, got %q", split.Type)
	}

	unchanged, found := splitLayoutNode(layout, "missing", "new", "vertical")
	if found {
		t.Fatal("expected missing target to be reported as not found")
	}
	if unchanged != layout {
		t.Fatal("expected original layout pointer to remain unchanged")
	}
}

func TestClosePaneInLayoutReportsTargetFound(t *testing.T) {
	layout := &LayoutNode{
		Type:      "split",
		Direction: "vertical",
		First:     &LayoutNode{Type: "pane", SessionID: "left"},
		Second:    &LayoutNode{Type: "pane", SessionID: "right"},
	}

	updated, found := closePaneInLayout(layout, "left")
	if !found {
		t.Fatal("expected close target to be found")
	}
	if updated == nil || updated.SessionID != "right" {
		t.Fatal("expected sibling pane to be promoted")
	}

	unchanged, found := closePaneInLayout(layout, "missing")
	if found {
		t.Fatal("expected missing close target to be reported as not found")
	}
	if unchanged != layout {
		t.Fatal("expected layout pointer to remain unchanged")
	}
}
