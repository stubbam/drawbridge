package service

import (
	"context"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
)

func TestSubscribersHearEventsWithTheirIDs(t *testing.T) {
	s, _ := newTestService(t)
	ctx := web(context.Background(), "admin")
	events, unsubscribe := s.Subscribe()
	defer unsubscribe()
	other, unsubscribeOther := s.Subscribe()
	defer unsubscribeOther()

	c, _, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetEnabled(ctx, store.ByID(c.ID), false); err != nil {
		t.Fatal(err)
	}

	stored, err := s.Events(ctx, store.EventFilter{})
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored %d events (err %v), want 2", len(stored), err)
	}
	for _, ch := range []<-chan store.Event{events, other} {
		for i, want := range []store.Event{stored[1], stored[0]} { // oldest first, as recorded
			select {
			case got := <-ch:
				if got.ID == 0 || got.ID != want.ID || got.Kind != want.Kind || got.ClientName != "phone" ||
					got.Actor != "admin" || got.SourceIP != "192.168.4.20" {
					t.Fatalf("event %d: %+v, want the stored %+v", i, got, want)
				}
			case <-time.After(time.Second):
				t.Fatalf("event %d never arrived", i)
			}
		}
	}

	unsubscribe()
	if _, _, err := s.SetEnabled(ctx, store.ByID(c.ID), true); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		t.Fatalf("an unsubscribed channel heard %+v", e)
	default:
	}
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("the other subscriber missed an event after the first left")
	}
}

func TestAStuckSubscriberDoesNotHoldUpEvents(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	_, unsubscribe := s.Subscribe() // never reads
	defer unsubscribe()
	reading, unsubscribeReading := s.Subscribe()
	defer unsubscribeReading()

	got := make(chan int, 1)
	go func() {
		n := 0
		for range reading {
			n++
			if n == 3*subscriberBuffer {
				got <- n
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 3 * subscriberBuffer {
			s.record(ctx, Event{Kind: "auth.logout"})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recording events waited for a subscriber that doesn't read")
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber that reads missed events because another one didn't")
	}
}

func TestAnEventTheDatabaseRefusedIsNotPublished(t *testing.T) {
	s, _ := newTestService(t)
	events, unsubscribe := s.Subscribe()
	defer unsubscribe()
	_ = s.Store.Close()
	s.record(context.Background(), Event{Kind: "auth.logout"})
	select {
	case e := <-events:
		t.Fatalf("heard %+v, which was never stored", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSnapshotIsTheStatusAndTheClients(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"phone", "tablet"} {
		if _, _, err := s.AddClient(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SetEnabled(ctx, store.ByName("tablet"), false); err != nil {
		t.Fatal(err)
	}
	st, clients, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st != want || !st.TunnelUp || st.Clients != 2 || st.Paused != 1 {
		t.Fatalf("snapshot status %+v, Status %+v", st, want)
	}
	if len(clients) != 2 || clients[0].Name != "phone" || clients[1].Name != "tablet" {
		t.Fatalf("snapshot clients %+v", clients)
	}
}
