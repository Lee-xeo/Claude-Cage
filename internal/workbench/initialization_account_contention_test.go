package workbench

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestInitializationRecordDoesNotWaitOnAccountAfterSafetyCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := initializationWaitServer(t)
		before := initializationWaitRecord(t, s)
		epoch, _ := s.account.access()
		r := initializationWaitHTTPRequest(s, context.WithValue(context.Background(), initializationEpochKey{}, epoch), "complete")
		s.account.mu.Lock()
		started := time.Now()
		// Exercise the final writer directly: the real middleware also reads
		// account.mu, which would obscure this specific post-check contention.
		err := s.writeInitialization(r, initializationRecord{Version: 1, Completed: true})
		s.account.mu.Unlock()
		if err == nil || time.Since(started) != 0 {
			t.Fatalf("record writer waited after safety validation: %v", err)
		}
		initializationWaitUnchanged(t, s, before)
	})
}
