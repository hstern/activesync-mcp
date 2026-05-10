package config

import (
	"reflect"
	"sort"
	"testing"
)

func TestCanWrite_defaults(t *testing.T) {
	a := &Account{DefaultAccess: AccessRO}
	for _, c := range AllClasses() {
		if a.CanWrite(c) {
			t.Errorf("default ro: CanWrite(%s) = true", c)
		}
	}
	a.DefaultAccess = AccessRW
	for _, c := range AllClasses() {
		if !a.CanWrite(c) {
			t.Errorf("default rw: CanWrite(%s) = false", c)
		}
	}
}

func TestCanWrite_perClassOverride(t *testing.T) {
	a := &Account{
		DefaultAccess: AccessRO,
		Access: map[string]string{
			"calendar": AccessRW,
			"tasks":    AccessRW,
		},
	}
	want := map[Class]bool{
		ClassEmail:    false,
		ClassCalendar: true,
		ClassContacts: false,
		ClassTasks:    true,
		ClassNotes:    false,
	}
	for c, w := range want {
		if got := a.CanWrite(c); got != w {
			t.Errorf("CanWrite(%s) = %v, want %v", c, got, w)
		}
	}
}

func TestCanWrite_overrideCanDowngrade(t *testing.T) {
	a := &Account{
		DefaultAccess: AccessRW,
		Access:        map[string]string{"email": AccessRO},
	}
	if a.CanWrite(ClassEmail) {
		t.Error("email should be ro despite default rw")
	}
	if !a.CanWrite(ClassCalendar) {
		t.Error("calendar should inherit default rw")
	}
}

func TestWritableAccounts(t *testing.T) {
	c := &Config{
		Accounts: []Account{
			{Name: "ro", DefaultAccess: AccessRO},
			{Name: "rw", DefaultAccess: AccessRW},
			{Name: "mixed", DefaultAccess: AccessRO, Access: map[string]string{"calendar": AccessRW}},
		},
	}
	got := c.WritableAccounts(ClassEmail)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"rw"}) {
		t.Errorf("email writable: %v", got)
	}
	got = c.WritableAccounts(ClassCalendar)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"mixed", "rw"}) {
		t.Errorf("calendar writable: %v", got)
	}
}

func TestAccountNames(t *testing.T) {
	c := &Config{Accounts: []Account{{Name: "a"}, {Name: "b"}}}
	if !reflect.DeepEqual(c.AccountNames(), []string{"a", "b"}) {
		t.Errorf("names: %v", c.AccountNames())
	}
}
