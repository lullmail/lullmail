package gmail

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// Gmail meters every call in quota units against a per-user budget. The
// numbers below are Google's published limits
// (developers.google.com/workspace/gmail/api/reference/quota); the per-method
// costs are what each call this adapter makes is charged.
//
// The arithmetic that motivates all of this: one messages.get costs 20 units,
// so a user gets 300 metadata fetches a minute, and a 500-message enumeration
// page costs 10,000 units. Fired as fast as the network allows, that page is
// throttled every time, and a throttled page is thrown away whole. Pacing
// calls to the budget is what turns "never finishes" into "finishes at the
// maximum rate Google permits".
//
// The per-second cap is the limit Google documented for years before moving
// the published figure to a per-minute one. It is still enforced in
// practice: spending the minute's allowance in a twenty-second burst is
// answered with rateLimitExceeded, which the docs describe only as a rate
// that "varies depending on the request type".
const (
	quotaWindow    = time.Minute
	quotaPerWindow = 6000
	burstWindow    = time.Second
	burstPerWindow = 250

	costLabelsList       = 1
	costGetProfile       = 1
	costHistoryList      = 2
	costMessagesList     = 5
	costMessagesGet      = 20
	costAttachmentsGet   = 20
	costMessagesBatchMod = 50
)

// budget is a sliding-window meter over one user's quota. A token bucket
// would be simpler, but a bucket that lets a full burst through admits up to
// twice the limit inside one window, which is exactly the overage Google
// counts. The windows keep the spend in any 60 seconds, and in any one
// second, at or under their caps.
type budget struct {
	mu    sync.Mutex
	spent []spend // ordered by at; nothing older than quotaWindow survives a wait
	total int     // units in spent
	now   func() time.Time
}

type spend struct {
	at    time.Time
	units int
}

// budgets is process-wide and keyed by account, because adapters are built
// per sync run and per UI request while Google meters the user across all of
// them. A budget that reset with each adapter would forget the run before.
var budgets sync.Map // account key -> *budget

func budgetFor(key string) *budget {
	if b, ok := budgets.Load(key); ok {
		return b.(*budget)
	}
	b, _ := budgets.LoadOrStore(key, &budget{now: time.Now})
	return b.(*budget)
}

// wait blocks until units can be spent without either trailing window
// exceeding its cap, then records the spend.
func (b *budget) wait(ctx context.Context, units int) error {
	for {
		b.mu.Lock()
		now := b.now()
		i := 0
		for i < len(b.spent) && now.Sub(b.spent[i].at) >= quotaWindow {
			b.total -= b.spent[i].units
			i++
		}
		b.spent = b.spent[i:]

		// Both windows share the log: the minute is the whole of it, the
		// second is its tail. Each violated window asks for the wait that
		// ages its oldest entry out; the longer wait satisfies both.
		var d time.Duration
		if b.total+units > quotaPerWindow && len(b.spent) > 0 {
			d = quotaWindow - now.Sub(b.spent[0].at)
		}
		recent := 0
		for j := len(b.spent) - 1; j >= 0 && now.Sub(b.spent[j].at) < burstWindow; j-- {
			recent += b.spent[j].units
			if recent+units > burstPerWindow {
				d = max(d, burstWindow-now.Sub(b.spent[j].at))
			}
		}
		if d == 0 {
			b.spent = append(b.spent, spend{at: now, units: units})
			b.total += units
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()

		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Throttle retries follow Google's recommendation: truncated exponential
// backoff with up to a second of jitter, then give up and let the scheduler
// back the whole account off. Six attempts (1+2+4+8+16+32 s) outlast a full
// quota minute on purpose: the budget lives in process memory, so a restart
// forgets the minute before while Google remembers it, and the first page
// after a restart should wait that minute out rather than be thrown away.
var (
	retryBase     = time.Second
	retryMax      = 32 * time.Second
	retryAttempts = 6
)

// call spends units from the account's budget, runs the request, and waits
// out a throttle in place. The budget is the plan; the retry is for when
// Google's accounting disagrees with ours, which it will, since other
// clients of the same user share the meter.
func (a *Adapter) call(ctx context.Context, units int, do func() error) error {
	backoff := retryBase
	for attempt := 0; ; attempt++ {
		if err := a.budget.wait(ctx, units); err != nil {
			return err
		}
		err := classify(do())
		if !errors.Is(err, mail.ErrRateLimited) || attempt == retryAttempts {
			return err
		}
		select {
		case <-time.After(backoff + rand.N(retryBase)):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < retryMax {
			backoff *= 2
		}
	}
}
