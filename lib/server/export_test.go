// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import "github.com/hstern/go-activesync/eas"

// SetClientForTest pre-populates the Manager's client cache for
// accountName with c, marked as already-provisioned. The next
// Manager.Client(accountName) returns c without going through
// account-config lookup, secret resolution, or the
// NegotiateVersion + Provision dance.
//
// Tests use this seam together with the github.com/hstern/go-activesync/eas/easmock
// fakes to exercise tool handlers without an httptest.Server. The
// _test.go suffix means production binaries cannot reach this method.
func (m *Manager) SetClientForTest(accountName string, c eas.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[accountName] = &managedClient{client: c, provisioned: true}
}
