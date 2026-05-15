package master

import (
	"testing"
	"time"
)

func TestNodeDirectoryObserveSetsFirstAndLastSeen(t *testing.T) {
	d := NewNodeDirectory()
	t1 := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	d.Observe("nas01", "nas01.lan", []string{"storage"}, t1)

	meta, ok := d.Get("nas01")
	if !ok {
		t.Fatal("expected node to exist")
	}
	if meta.FirstSeen == nil || !meta.FirstSeen.Equal(t1) {
		t.Fatalf("FirstSeen=%v want %v", meta.FirstSeen, t1)
	}
	if meta.LastSeen == nil || !meta.LastSeen.Equal(t1) {
		t.Fatalf("LastSeen=%v want %v", meta.LastSeen, t1)
	}
	if meta.Hostname != "nas01.lan" {
		t.Fatalf("Hostname=%q", meta.Hostname)
	}
}

func TestNodeDirectoryObserveTwiceKeepsFirstSeen(t *testing.T) {
	d := NewNodeDirectory()
	t1 := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Minute)
	d.Observe("nas01", "nas01.lan", []string{"storage"}, t1)
	d.Observe("nas01", "nas01.lan", []string{"storage"}, t2)

	meta, _ := d.Get("nas01")
	if !meta.FirstSeen.Equal(t1) {
		t.Fatalf("FirstSeen should be initial observation")
	}
	if !meta.LastSeen.Equal(t2) {
		t.Fatalf("LastSeen should be latest observation")
	}
}

func TestNodeDirectoryTouchUpdatesLastSeenOnly(t *testing.T) {
	d := NewNodeDirectory()
	t1 := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	d.Observe("nas01", "nas01.lan", nil, t1)

	t2 := t1.Add(time.Minute)
	d.Touch("nas01", t2)
	meta, _ := d.Get("nas01")
	if !meta.LastSeen.Equal(t2) {
		t.Fatalf("LastSeen not updated by Touch")
	}

	d.Touch("missing", t2)
	if _, ok := d.Get("missing"); ok {
		t.Fatalf("Touch must not create new entry")
	}
}

func TestNodeDirectorySetExpectedCreatesAndMarks(t *testing.T) {
	d := NewNodeDirectory()
	d.SetExpected("printer", "printer.lan", []string{"office"})

	meta, ok := d.Get("printer")
	if !ok {
		t.Fatal("expected node to exist")
	}
	if !meta.Expected {
		t.Fatal("Expected flag should be true")
	}
	if meta.FirstSeen != nil {
		t.Fatal("FirstSeen should be nil for never-seen expected node")
	}
}

func TestNodeDirectoryDeleteRefusesSeenNode(t *testing.T) {
	d := NewNodeDirectory()
	d.Observe("nas01", "nas01.lan", nil, time.Now().UTC())

	if got := d.Delete("nas01"); got != DeleteRefusedSeen {
		t.Fatalf("Delete=%d want DeleteRefusedSeen", got)
	}
}

func TestNodeDirectoryDeleteAllowsNeverSeenNode(t *testing.T) {
	d := NewNodeDirectory()
	d.SetExpected("printer", "", nil)

	if got := d.Delete("printer"); got != DeleteOK {
		t.Fatalf("Delete=%d want DeleteOK", got)
	}
	if _, ok := d.Get("printer"); ok {
		t.Fatal("node should be gone")
	}
}

func TestNodeDirectoryListReturnsDeepCopies(t *testing.T) {
	d := NewNodeDirectory()
	d.Observe("nas01", "nas01.lan", []string{"storage"}, time.Now().UTC())

	list := d.List()
	if len(list) != 1 {
		t.Fatalf("list len=%d", len(list))
	}
	list[0].Tags[0] = "mutated"
	list[0].Hostname = "mutated"

	meta, _ := d.Get("nas01")
	if meta.Hostname != "nas01.lan" || meta.Tags[0] != "storage" {
		t.Fatal("List() returned shared references; internal state mutated")
	}
}
