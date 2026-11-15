package realtime

import (
	"testing"
	"time"
)

func TestHubFansOutPerUser(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	first, err := hub.Subscribe(7)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := hub.Subscribe(7)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	other, err := hub.Subscribe(8)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	hub.Publish(7, Event{Name: "unread", Data: []byte(`{"count":1}`)})

	for _, sub := range []*Subscriber{first, second} {
		select {
		case ev := <-sub.Events():
			if ev.Name != "unread" || string(ev.Data) != `{"count":1}` {
				t.Fatalf("unexpected event %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive the event")
		}
	}
	select {
	case ev := <-other.Events():
		t.Fatalf("other user received %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubPerUserLimit(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	for i := 0; i < DefaultPerUser; i++ {
		sub, err := hub.Subscribe(1)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		defer sub.Close()
	}
	if _, err := hub.Subscribe(1); err != ErrTooManyStreams {
		t.Fatalf("expected ErrTooManyStreams, got %v", err)
	}
	// Another account is unaffected.
	sub, err := hub.Subscribe(2)
	if err != nil {
		t.Fatalf("second account: %v", err)
	}
	sub.Close()
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	sub, err := hub.Subscribe(1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= subscriberBuffer; i++ {
		hub.Publish(1, Event{Name: "unread", Data: []byte(`{}`)})
	}
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("slow subscriber was not closed")
	}
	if _, total := hub.Counters(); total != 0 {
		t.Fatalf("slow subscriber still registered: %d", total)
	}
	sub.Close()
}

func TestHubCloseRejectsAndReleases(t *testing.T) {
	hub := NewHub()
	sub, err := hub.Subscribe(1)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	hub.Close()
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("subscriber was not released by Close")
	}
	if _, err := hub.Subscribe(1); err != ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}
