package aws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBudgetIsSharedAndHeldUntilBodyFinishes(t *testing.T) {
	budget := NewBudget()
	budget.limit = rate.Inf
	budget.concurrency = 1
	var calls atomic.Int64
	denied := errors.New("lease revoked")
	var revoke atomic.Bool
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if revoke.Load() {
			return nil, denied
		}
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("payload")), Header: make(http.Header)}, nil
	})
	a := budget.Wrap(base, "account-a", "account-a", "us-east-1", "us-east-1")
	same := budget.Wrap(base, "account-a", "account-a", "us-east-1", "us-east-1")
	other := budget.Wrap(base, "account-b", "account-b", "us-east-1", "us-east-1")
	request, _ := http.NewRequest("GET", "https://lightsail.us-east-1.amazonaws.com/", nil)
	first, err := a.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := same.RoundTrip(request.WithContext(ctx)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("connection caches did not share the budget")
	}
	second, err := other.RoundTrip(request)
	if err != nil {
		t.Fatal("one account blocked another", err)
	}
	second.Body.Close()
	first.Body.Close()
	revoke.Store(true)
	if _, err := same.RoundTrip(request); !errors.Is(err, denied) {
		t.Fatal("inner authorization guard was bypassed")
	}
	if calls.Load() != 2 {
		t.Fatal("blocked or revoked request reached the network")
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	for _, entry := range budget.entries {
		if entry.refs != 0 {
			t.Fatal("budget slot was not released")
		}
	}
}
