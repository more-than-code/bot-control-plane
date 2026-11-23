package api

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// An agent_upgrade problem links to its host with a fragment, so the console's bell can tell it from
// host_offline (bell items carry no kind; api-v1 v1.48 item 6).
func TestProblemHrefHostKinds(t *testing.T) {
	host := uuid.New()
	r := &problemReader{}
	for _, c := range []struct {
		kind string
		want string
	}{
		{store.ProblemHostOffline, "/hosts/" + host.String()},
		{store.ProblemAgentUpgrade, "/hosts/" + host.String() + "#agent-upgrade"},
	} {
		if got := r.href(context.Background(), store.Problem{Kind: c.kind, HostID: &host}); got != c.want {
			t.Errorf("%s href = %q, want %q", c.kind, got, c.want)
		}
	}
}
