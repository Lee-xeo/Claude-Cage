package workbench

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestNetworkMonitorWaitsAfterSlowPhysicalProbe(t *testing.T) {
	for _, probeDuration := range []time.Duration{6 * time.Second, 10 * time.Second} {
		t.Run(probeDuration.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testServer(t)
				previous := physicalProxyInterface
				defer func() { physicalProxyInterface = previous }()
				var starts []time.Time
				physicalProxyInterface = func(context.Context) (string, error) {
					starts = append(starts, time.Now())
					if len(starts) == 1 {
						time.Sleep(probeDuration)
					}
					return "en0", nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); s.monitorNetwork(ctx) }()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				if len(starts) != 1 {
					t.Fatalf("initial monitor should run immediately, starts=%d", len(starts))
				}
				if s.op.TryLock() {
					s.op.Unlock()
					t.Fatal("fixture did not hold the lifecycle lock during the probe")
				}
				time.Sleep(probeDuration)
				synctest.Wait()
				if len(starts) != 1 {
					t.Fatalf("slow probe triggered an immediate catch-up round: %d", len(starts))
				}
				if !s.op.TryLock() {
					t.Fatal("lifecycle lock was not released between rounds")
				}
				s.op.Unlock()
				time.Sleep(5*time.Second - time.Nanosecond)
				synctest.Wait()
				if len(starts) != 1 {
					t.Fatal("monitor restarted before the full post-round interval")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if len(starts) != 2 || starts[1].Sub(starts[0]) != probeDuration+5*time.Second {
					t.Fatalf("wrong post-round schedule: %v", starts)
				}
			})
		})
	}
}
