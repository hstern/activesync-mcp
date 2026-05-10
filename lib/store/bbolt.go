// Package store provides durable per-account state persistence for the
// activesync-mcp server. The persistence layer is bbolt; per-account
// views satisfy the eas.StateStore interface so the EAS client can
// remain storage-agnostic (and library-extractable).
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hstern/go-activesync/eas"

	"go.etcd.io/bbolt"
)

// Bucket layout
//
//	policykey   -> { account => key }
//	synckey     -> { account => bucket of { folderID => key } }
const (
	bucketPolicyKey = "policykey"
	bucketSyncKey   = "synckey"
)

// DB wraps a single bbolt database serving all configured accounts.
type DB struct {
	db *bbolt.DB
}

// Open creates or opens the bbolt file at path, creating the parent
// directory if needed. Buckets are created on first open.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create state dir: %w", err)
	}
	bdb, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := bdb.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{bucketPolicyKey, bucketSyncKey} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = bdb.Close()
		return nil, fmt.Errorf("store: create buckets: %w", err)
	}
	return &DB{db: bdb}, nil
}

// Close releases the file lock and flushes any in-flight writes.
func (d *DB) Close() error { return d.db.Close() }

// AccountState returns an eas.StateStore scoped to the given account name.
func (d *DB) AccountState(account string) eas.StateStore {
	return &accountState{db: d.db, account: []byte(account)}
}

// accountState satisfies eas.StateStore by indexing into the shared bbolt
// buckets with the account name.
type accountState struct {
	db      *bbolt.DB
	account []byte
}

func (a *accountState) PolicyKey(_ context.Context) (string, error) {
	var out string
	err := a.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketPolicyKey))
		if b == nil {
			return nil
		}
		if v := b.Get(a.account); v != nil {
			out = string(v)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: PolicyKey: %w", err)
	}
	return out, nil
}

func (a *accountState) SetPolicyKey(_ context.Context, key string) error {
	if err := a.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketPolicyKey))
		if key == "" {
			return b.Delete(a.account)
		}
		return b.Put(a.account, []byte(key))
	}); err != nil {
		return fmt.Errorf("store: SetPolicyKey: %w", err)
	}
	return nil
}

func (a *accountState) SyncKey(_ context.Context, folderID string) (string, error) {
	out := "0"
	err := a.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketSyncKey))
		if root == nil {
			return nil
		}
		acct := root.Bucket(a.account)
		if acct == nil {
			return nil
		}
		if v := acct.Get([]byte(folderID)); v != nil {
			out = string(v)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: SyncKey: %w", err)
	}
	return out, nil
}

func (a *accountState) SetSyncKey(_ context.Context, folderID, key string) error {
	if err := a.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketSyncKey))
		acct, err := root.CreateBucketIfNotExists(a.account)
		if err != nil {
			return err
		}
		if key == "" || key == "0" {
			return acct.Delete([]byte(folderID))
		}
		return acct.Put([]byte(folderID), []byte(key))
	}); err != nil {
		return fmt.Errorf("store: SetSyncKey: %w", err)
	}
	return nil
}

// ResetAccount drops every persisted key for the given account. Used by
// the doctor / debug paths if a server-side state is corrupt and a full
// resync is the cleanest recovery.
func (d *DB) ResetAccount(account string) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		if b := tx.Bucket([]byte(bucketPolicyKey)); b != nil {
			if err := b.Delete([]byte(account)); err != nil {
				return err
			}
		}
		if b := tx.Bucket([]byte(bucketSyncKey)); b != nil {
			if sub := b.Bucket([]byte(account)); sub != nil {
				if err := b.DeleteBucket([]byte(account)); err != nil {
					return err
				}
				_ = sub
			}
		}
		return nil
	})
}
