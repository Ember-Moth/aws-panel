package aws

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Budget shares limits across connection caches and members in this process.
// Separate account/region/service keys prevent one scope from exhausting others.
type Budget struct {
	mu                 sync.Mutex
	entries            map[string]*budgetEntry
	limit              rate.Limit
	burst, concurrency int
}
type budgetEntry struct {
	limiter *rate.Limiter
	slots   chan struct{}
	refs    int
	used    time.Time
}

func NewBudget() *Budget {
	return &Budget{entries: map[string]*budgetEntry{}, limit: 4, burst: 4, concurrency: 2}
}
func (b *Budget) entry(key string) (*budgetEntry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[key]
	if entry == nil {
		if len(b.entries) >= 1024 {
			oldest := ""
			var at time.Time
			for candidate, value := range b.entries {
				if value.refs == 0 && (oldest == "" || value.used.Before(at)) {
					oldest, at = candidate, value.used
				}
			}
			if oldest == "" {
				return nil, errors.New("AWS request budget is busy")
			}
			delete(b.entries, oldest)
		}
		entry = &budgetEntry{limiter: rate.NewLimiter(b.limit, b.burst), slots: make(chan struct{}, b.concurrency)}
		b.entries[key] = entry
	}
	entry.refs++
	entry.used = time.Now()
	return entry, nil
}

type budgetTransport struct {
	budget                                 *Budget
	account, stsAccount, region, stsRegion string
	base                                   http.RoundTripper
}

var endpointRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func (b *Budget) Wrap(base http.RoundTripper, account, stsAccount, region, stsRegion string) http.RoundTripper {
	return &budgetTransport{budget: b, base: base, account: account, stsAccount: stsAccount, region: region, stsRegion: stsRegion}
}
func (t *budgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	service, _, _ := strings.Cut(request.URL.Hostname(), ".")
	account, region := t.account, t.region
	if strings.HasPrefix(service, "sts") && t.stsAccount != "" {
		account, region = t.stsAccount, t.stsRegion
	}
	parts := strings.Split(request.URL.Hostname(), ".")
	if len(parts) > 2 && endpointRegion.MatchString(parts[1]) {
		region = parts[1]
	}
	entry, err := t.budget.entry(account + "/" + region + "/" + service)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	slot := false
	release := func() {
		once.Do(func() {
			if slot {
				<-entry.slots
			}
			t.budget.mu.Lock()
			entry.refs--
			entry.used = time.Now()
			t.budget.mu.Unlock()
		})
	}
	select {
	case entry.slots <- struct{}{}:
		slot = true
	case <-request.Context().Done():
		release()
		return nil, request.Context().Err()
	}
	if err := entry.limiter.Wait(request.Context()); err != nil {
		release()
		return nil, err
	}
	// The inner guard executes after budget waiting, so revoked permissions or
	// expired task leases cannot be bypassed by a request waiting for a slot.
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		release()
	} else {
		response.Body = &budgetBody{ReadCloser: response.Body, release: release}
	}
	return response, err
}

type budgetBody struct {
	io.ReadCloser
	release func()
}

func (b *budgetBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}
func (b *budgetBody) Close() error { defer b.release(); return b.ReadCloser.Close() }
func (t *budgetTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
