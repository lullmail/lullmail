package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
	"google.golang.org/api/option"
)

func TestBudgetCapsSpendPerSecondAndPerMinute(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	b := &budget{now: func() time.Time { return now }}
	ctx := context.Background()
	blocks := func(units int) bool {
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		return errors.Is(b.wait(short, units), context.DeadlineExceeded)
	}

	// Within one second, the burst cap admits 12 fetches and blocks the 13th.
	perSecond := burstPerWindow / costMessagesGet
	for i := range perSecond {
		if err := b.wait(ctx, costMessagesGet); err != nil {
			t.Fatalf("fetch %d within the burst cap blocked: %v", i, err)
		}
	}
	if !blocks(costMessagesGet) {
		t.Fatal("a fetch over the per-second cap went through")
	}

	// Spread over enough seconds, the minute's allowance is spent exactly.
	for spent := perSecond * costMessagesGet; spent < quotaPerWindow; spent += costMessagesGet {
		if (spent/costMessagesGet)%perSecond == 0 {
			now = now.Add(burstWindow)
		}
		if err := b.wait(ctx, costMessagesGet); err != nil {
			t.Fatalf("fetch at %d units within the minute blocked: %v", spent, err)
		}
	}
	now = now.Add(burstWindow)
	if !blocks(costGetProfile) {
		t.Fatal("a spend over the per-minute cap went through")
	}

	// Once the oldest spend ages out of the minute, room reappears.
	now = now.Add(quotaWindow)
	if err := b.wait(ctx, costMessagesGet); err != nil {
		t.Fatalf("spend after the window elapsed blocked: %v", err)
	}
}

func TestCallWaitsOutAThrottleInPlace(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = time.Second })

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		if hits == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"User-rate limit exceeded"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"labels":[{"id":"INBOX","name":"INBOX"}]}`)
	}))
	defer server.Close()

	adapter, err := New(context.Background(), t.Name(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := adapter.Mailboxes(context.Background())
	if err != nil {
		t.Fatalf("a single throttle surfaced as %v; want a retried success", err)
	}
	if hits != 2 || len(boxes) != 1 {
		t.Fatalf("hits = %d, boxes = %d; want one retry and one label", hits, len(boxes))
	}
}

func TestCallGivesUpAfterSustainedThrottle(t *testing.T) {
	retryBase = time.Millisecond
	retryMax = time.Millisecond
	t.Cleanup(func() { retryBase, retryMax = time.Second, 32*time.Second })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"User-rate limit exceeded"}}`)
	}))
	defer server.Close()

	adapter, err := New(context.Background(), t.Name(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Mailboxes(context.Background()); !errors.Is(err, mail.ErrRateLimited) {
		t.Fatalf("sustained throttle returned %v, want ErrRateLimited for the scheduler to back off", err)
	}
}
