package github

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestFailures(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	exp := now.Add(15 * time.Minute)

	var none *Failures
	none.Record([]byte("a"), ReasonScopeMissing, now, exp)
	none.Clear([]byte("a"))
	if none.Last([]byte("a"), now) != nil {
		t.Fatal("nil map read a failure")
	}

	f := NewFailures()
	f.Record([]byte("a"), ReasonExpiredSignIn, now, exp)
	f.Record([]byte("a"), ReasonScopeMissing, now.Add(time.Second), exp)
	if got := f.Last([]byte("a"), now.Add(time.Second)); got == nil || got.Reason != ReasonScopeMissing || !got.At.Equal(now.Add(time.Second)) {
		t.Fatalf("last %v", got)
	}
	if f.Last([]byte("a"), exp) != nil {
		t.Fatal("failure outlived its request")
	}
	f.Clear([]byte("a"))
	if f.Len() != 0 {
		t.Fatal("not cleared")
	}
	// Already expired: nothing kept.
	f.Record([]byte("b"), ReasonForbidden, exp, exp)
	if f.Len() != 0 {
		t.Fatal("expired failure kept")
	}

	// The cap: a full map sweeps expired entries, else records nothing new.
	for i := 0; i < failureCap; i++ {
		f.Record([]byte(strconv.Itoa(i)), ReasonGitHubError, now, exp)
	}
	f.Record([]byte("new"), ReasonGitHubError, now, exp)
	if f.Len() != failureCap || f.Last([]byte("new"), now) != nil {
		t.Fatalf("over the cap: %d", f.Len())
	}
	f.Record([]byte("0"), ReasonSaveFailed, now, exp)
	if got := f.Last([]byte("0"), now); got == nil || got.Reason != ReasonSaveFailed {
		t.Fatal("an existing entry was not updated at the cap")
	}
	later := exp.Add(time.Minute)
	f.Record([]byte("new"), ReasonGitHubError, later, later.Add(time.Minute))
	if f.Len() != 1 || f.Last([]byte("new"), later) == nil {
		t.Fatalf("expired entries not swept at the cap: %d", f.Len())
	}
}

func TestFailuresNilLen(t *testing.T) {
	var none *Failures
	if none.Len() != 0 {
		t.Fatal("nil Len")
	}
}

// TestFailuresSweep: an expired entry is dropped by the once-a-minute sweep
// on a later Record, with the map far below the cap.
func TestFailuresSweep(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := NewFailures()
	f.Record([]byte("old"), ReasonGitHubError, now, now.Add(10*time.Second))
	f.Record([]byte("live"), ReasonGitHubError, now, now.Add(time.Hour))
	// Expired, but the last sweep was less than a minute ago: still held.
	f.Record([]byte("live"), ReasonScopeMissing, now.Add(30*time.Second), now.Add(time.Hour))
	if f.Len() != 2 {
		t.Fatalf("swept early: %d", f.Len())
	}
	f.Record([]byte("live"), ReasonForbidden, now.Add(sweepEvery+time.Second), now.Add(time.Hour))
	if f.Len() != 1 || f.Last([]byte("live"), now.Add(sweepEvery+time.Second)) == nil {
		t.Fatalf("expired entry not swept: %d", f.Len())
	}
}

// TestFailuresParallel: Record, Last, Clear and Len from several goroutines
// (run with -race).
func TestFailuresParallel(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := NewFailures()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := []byte(strconv.Itoa(i % 16))
				at := now.Add(time.Duration(g*200+i) * time.Second)
				f.Record(key, ReasonGitHubError, at, at.Add(time.Hour))
				_ = f.Last(key, at)
				if i%7 == 0 {
					f.Clear(key)
				}
				_ = f.Len()
			}
		}(g)
	}
	wg.Wait()
	if n := f.Len(); n > 16 {
		t.Fatalf("%d entries for 16 keys", n)
	}
}
