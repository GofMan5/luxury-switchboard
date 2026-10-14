package application

import (
	"context"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestOldLeaseCannotPenalizeReplacementKey(t *testing.T) {
	for _, change := range []string{"reinsert", "proxy", "metadata"} {
		t.Run(change, func(t *testing.T) {
			scheduler := NewScheduler(10)
			key := testKey(t, "fixture", "key", "synthetic-credential", 0, 0)
			if err := scheduler.Configure("fixture", 0, 0, []domain.Key{key}); err != nil {
				t.Fatal(err)
			}
			lease, _, err := scheduler.Acquire(context.Background(), "fixture", "model")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "reinsert":
				if err := scheduler.Configure("fixture", 0, 0, nil); err != nil {
					t.Fatal(err)
				}
			case "proxy":
				key.ProxyURL = "http://127.0.0.1:1"
			case "metadata":
				key.Label = "renamed"
				key.RPM = 5
			}
			if err := scheduler.Configure("fixture", 0, 0, []domain.Key{key}); err != nil {
				t.Fatal(err)
			}
			lease.Finish(domain.Outcome{Kind: domain.OutcomeAuthentication})
			lease.Finish(domain.Outcome{Kind: domain.OutcomeAuthentication})
			got := scheduler.Snapshot("fixture")[0]
			if change == "metadata" {
				if got.AuthStreak != 1 || got.CooldownMS == 0 {
					t.Fatalf("unchanged credential/egress lost actual outcome: %+v", got)
				}
			} else if got.AuthStreak != 0 || got.CooldownMS != 0 || got.LastOutcome != "" {
				t.Fatalf("old incarnation penalized replacement: %+v", got)
			}
		})
	}
}
