// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package osutil

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestLimaUserChecksDeferredCurrentUserError(t *testing.T) {
	origUser, origErr := currentUser, currentUserErr
	origOnce, origLimaUser, origWarnings := once, limaUser, warnings
	t.Cleanup(func() {
		currentUser, currentUserErr = origUser, origErr
		once, limaUser, warnings = origOnce, origLimaUser, origWarnings
	})

	sentinel := errors.New("profile unavailable")
	currentUser, currentUserErr = nil, sentinel
	once = new(sync.Once)
	limaUser = nil
	warnings = nil

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected LimaUser to panic when current user lookup failed")
		}
		err, ok := r.(error)
		if !ok || !errors.Is(err, sentinel) {
			t.Fatalf("unexpected panic value: %#v", r)
		}
	}()
	_ = LimaUser(context.Background(), "2.0.0", false, nil)
}
