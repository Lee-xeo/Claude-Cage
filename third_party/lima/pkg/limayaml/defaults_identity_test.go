// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package limayaml

import (
	"errors"
	"testing"
)

func TestExecuteHostTemplateReturnsDeferredIdentityErrors(t *testing.T) {
	origUser, origUserErr := currentUser, currentUserErr
	origHome, origHomeErr := userHomeDir, userHomeDirErr
	t.Cleanup(func() {
		currentUser, currentUserErr = origUser, origUserErr
		userHomeDir, userHomeDirErr = origHome, origHomeErr
	})

	userSentinel := errors.New("current user unavailable")
	currentUser, currentUserErr = nil, userSentinel
	if _, err := executeHostTemplate("{{.User}}", "/tmp/inst", nil); !errors.Is(err, userSentinel) {
		t.Fatalf("got err=%v, want wrapped %v", err, userSentinel)
	}

	homeSentinel := errors.New("home dir unavailable")
	currentUser, currentUserErr = origUser, nil
	userHomeDir, userHomeDirErr = "", homeSentinel
	if _, err := executeHostTemplate("{{.Home}}", "/tmp/inst", nil); !errors.Is(err, homeSentinel) {
		t.Fatalf("got err=%v, want wrapped %v", err, homeSentinel)
	}
}
