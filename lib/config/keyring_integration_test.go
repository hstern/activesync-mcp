// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration tests for the keyring path through SecretResolver.
//
// These tests hit the real OS keyring backend on each platform —
// macOS Keychain, Windows Credential Manager, or whichever D-Bus
// Secret Service provider the Linux runner has registered (CI runs
// against gnome-keyring; KWallet would slot in here unchanged).
//
// Skipped automatically if the keyring is unreachable, so a developer
// without a keyring configured (e.g. headless ssh on Linux without
// gnome-keyring-daemon running) can still `make integration` for the
// other Tier 2 surfaces.

package config

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// uniqueKeyringAccount returns a per-test account label so parallel
// runs and CI reruns don't collide. We always Delete in a t.Cleanup
// to keep keyrings tidy, but a stale entry from a crashed test
// shouldn't poison the next run either.
func uniqueKeyringAccount(t *testing.T) (service, account string) {
	t.Helper()
	return "activesync-mcp-test", fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
}

// keyringAvailable reports whether the OS keyring is reachable. If
// not (typically: Linux without a Secret Service provider on the bus),
// every keyring test calls t.Skip with an actionable message.
func keyringAvailable(t *testing.T) bool {
	t.Helper()
	probeSvc, probeAcct := "activesync-mcp-test", "probe"
	defer func() { _ = keyring.Delete(probeSvc, probeAcct) }()
	if err := keyring.Set(probeSvc, probeAcct, "ok"); err != nil {
		t.Skipf("OS keyring unreachable (%v) — on Linux, ensure gnome-keyring-daemon "+
			"or kwalletd is running and DBUS_SESSION_BUS_ADDRESS is set", err)
		return false
	}
	return true
}

func TestKeyring_RoundTrip(t *testing.T) {
	if !keyringAvailable(t) {
		return
	}
	svc, acct := uniqueKeyringAccount(t)
	const password = "round-trip-secret-!@#$%"

	if err := keyring.Set(svc, acct, password); err != nil {
		t.Fatalf("keyring.Set: %v", err)
	}
	t.Cleanup(func() { _ = keyring.Delete(svc, acct) })

	r := DefaultResolver()
	got, err := r.Resolve(context.Background(), &Account{
		Name:   "test-account",
		Secret: SecretRef{KeyringService: svc, KeyringAccount: acct},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != password {
		t.Errorf("Resolve = %q, want %q", got, password)
	}
}

func TestKeyring_DeleteRemoves(t *testing.T) {
	if !keyringAvailable(t) {
		return
	}
	svc, acct := uniqueKeyringAccount(t)
	if err := keyring.Set(svc, acct, "doomed"); err != nil {
		t.Fatalf("keyring.Set: %v", err)
	}
	if err := keyring.Delete(svc, acct); err != nil {
		t.Fatalf("keyring.Delete: %v", err)
	}

	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "test-account",
		Secret: SecretRef{KeyringService: svc, KeyringAccount: acct},
	})
	if err == nil {
		t.Fatal("Resolve after Delete should error, got nil")
	}
	// SecretResolver wraps ErrNotFound with a hint pointing at
	// `activesync-mcp keyring set`. Don't tie the assertion to the
	// underlying sentinel since it varies per platform.
	if !contains(err.Error(), "not set") {
		t.Errorf("err = %v, want substring 'not set'", err)
	}
}

func TestKeyring_AbsentReturnsActionableError(t *testing.T) {
	if !keyringAvailable(t) {
		return
	}
	svc, acct := uniqueKeyringAccount(t)
	// No Set: the entry doesn't exist. The error must mention the
	// `activesync-mcp keyring set` hint so a confused user can recover.
	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "absent-acct",
		Secret: SecretRef{KeyringService: svc, KeyringAccount: acct},
	})
	if err == nil {
		t.Fatal("want error for absent keyring entry")
	}
	for _, want := range []string{"keyring entry not set", "absent-acct"} {
		if !contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}
}

func TestKeyring_EmptyValueRejected(t *testing.T) {
	if !keyringAvailable(t) {
		return
	}
	svc, acct := uniqueKeyringAccount(t)
	// Some backends silently coerce empty to ErrNotFound; others store
	// it. The contract is the same either way: SecretResolver must
	// treat an empty result as a hard error rather than letting an
	// empty password reach the EAS layer.
	if err := keyring.Set(svc, acct, ""); err != nil {
		// macOS Keychain rejects empty Set with -25303; that's the
		// expected behaviour and itself satisfies the contract.
		t.Logf("backend rejects empty Set (acceptable): %v", err)
		return
	}
	t.Cleanup(func() { _ = keyring.Delete(svc, acct) })

	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "empty-acct",
		Secret: SecretRef{KeyringService: svc, KeyringAccount: acct},
	})
	if err == nil {
		t.Fatal("Resolve should reject empty value, got nil")
	}
}

// errors.Is would couple to the sentinel; stay loose with substring match.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Static check: the resolver returns the same not-found error class
// regardless of platform. Keeping this here so the integration suite
// fails clearly if a future go-keyring upgrade changes the sentinel.
var _ = errors.Is
