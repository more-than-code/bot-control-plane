package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
	"github.com/more-than-code/bot-control-plane/mcp/internal/auth"
)

const endsByProject = "11111111-1111-4111-8111-111111111111"

// runningEndsByAPI serves a hosts list of n deploy hosts. The host with index
// running has an in-flight first row; each history read waits delay.
func runningEndsByAPI(t *testing.T, n, running int, delay time.Duration) (*apiclient.Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	reads := 0
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("22222222-2222-4222-8222-%012d", i)
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/hosts") {
			items := make([]string, n)
			for i, id := range ids {
				items[i] = fmt.Sprintf(`{"id":%q,"access_level":"deploy"}`, id)
			}
			_, _ = io.WriteString(w, `{"code":"ok","msg":"ok","payload":{"items":[`+strings.Join(items, ",")+`]}}`)
			return
		}
		mu.Lock()
		reads++
		mu.Unlock()
		time.Sleep(delay)
		if running >= 0 && r.URL.Query().Get("host_id") == ids[running] {
			_, _ = io.WriteString(w, `{"code":"ok","msg":"ok","payload":{"items":[{"outcome":"in_flight","ends_by":"2026-11-21T05:09:00Z"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":"ok","msg":"ok","payload":{"items":[]}}`)
	}))
	t.Cleanup(fake.Close)
	api, err := apiclient.New(fake.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	return api, func() int {
		mu.Lock()
		defer mu.Unlock()
		return reads
	}
}

// spec-deploy-stuck §9 item 4 (review finding 8): the lookup reads the
// histories in parallel within its 3 s bound, and names no time when the
// project has more than 5 deploy hosts.
func TestRunningEndsByLookup(t *testing.T) {
	ctx := auth.WithDelegatedToken(context.Background(), "test-delegated", nil)

	api, reads := runningEndsByAPI(t, 5, 3, 500*time.Millisecond)
	start := time.Now()
	if got := runningEndsBy(ctx, api, endsByProject, "web"); got != "2026-11-21T05:09:00Z" {
		t.Fatalf("ends_by %q", got)
	}
	if took := time.Since(start); took > 2*time.Second || reads() != 5 {
		t.Fatalf("five reads took %s (%d reads); not parallel", took, reads())
	}

	api, reads = runningEndsByAPI(t, 6, 0, 0)
	if got := runningEndsBy(ctx, api, endsByProject, "web"); got != "" || reads() != 0 {
		t.Fatalf("six hosts named %q after %d reads", got, reads())
	}

	api, _ = runningEndsByAPI(t, 2, 0, 4*time.Second)
	start = time.Now()
	if got := runningEndsBy(ctx, api, endsByProject, "web"); got != "" {
		t.Fatalf("slow lookup named %q", got)
	}
	if took := time.Since(start); took > runningLookupTimeout+time.Second {
		t.Fatalf("lookup took %s, past its bound", took)
	}
}
