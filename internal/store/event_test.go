package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestEventsSeqFilterAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbx.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := old.Add(48 * time.Hour)
	for _, ev := range []Event{
		{Type: "sandbox.created", SandboxID: "sbx_a", OwnerKeyID: "k1", Time: old},
		{Type: "sandbox.created", SandboxID: "sbx_b", OwnerKeyID: "k2", Time: now},
		{Type: "sandbox.lost", SandboxID: "sbx_a", OwnerKeyID: "k1", Time: now, Data: map[string]any{"reason": "missing"}},
	} {
		if _, err := st.InsertEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListEvents(ctx, EventFilter{OwnerID: "k1"})
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 3 || got[1].Data["reason"] != "missing" {
		t.Fatalf("owner filter %+v %v", got, err)
	}
	if got, _ := st.ListEvents(ctx, EventFilter{After: 1, SandboxID: "sbx_a"}); len(got) != 1 || got[0].Type != "sandbox.lost" {
		t.Fatalf("cursor %+v", got)
	}
	if n, err := st.PruneEvents(ctx, old.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune %d %v", n, err)
	}
	// Seq keeps growing after a prune and a reopen, so cursors stay valid.
	st.Close()
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.PruneEvents(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ev, err := st.InsertEvent(ctx, Event{Type: "sandbox.created", SandboxID: "sbx_c", OwnerKeyID: "k1", Time: now})
	if err != nil || ev.Seq != 4 {
		t.Fatalf("seq after prune %d %v", ev.Seq, err)
	}
	if last, _ := st.LastEventSeq(ctx); last != 4 {
		t.Fatalf("last %d", last)
	}
}
