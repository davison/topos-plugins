package main

import (
	"errors"
	"testing"
)

// TestAllowHost_PredicateTable proves the outbound host allowlist directly:
// a client built against a given base URL permits that hostname (any letter
// case, any port), loopback addresses, and the literal "localhost"; and
// refuses a foreign hostname, a foreign non-loopback IP literal, and an
// empty host. Every refusal must satisfy errors.Is(err, ErrForeignHost) —
// the test is on the sentinel specifically, not "any error". Mirrors
// plugins/silverbullet/outbound_hosts_test.go's TestAllowHost_PredicateTable
// in shape, adapted to the proton client's IMAP-scheme NewClient signature.
func TestAllowHost_PredicateTable(t *testing.T) {
	c, err := NewClient("imap://notes.example.lan:1143", "test-user", "test-pass", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	permit := []string{
		"notes.example.lan",
		"NOTES.EXAMPLE.LAN",
		"notes.example.lan:9000",
		"127.0.0.1",
		"127.0.0.1:8080",
		"::1",
		"[::1]:8080",
		"localhost",
		"LOCALHOST",
	}
	for _, host := range permit {
		if err := c.allowHost(host); err != nil {
			t.Errorf("allowHost(%q): expected nil, got %v", host, err)
		}
	}

	refuse := []string{
		"exfil.example.invalid",
		"203.0.113.5",
		"",
	}
	for _, host := range refuse {
		err := c.allowHost(host)
		if err == nil {
			t.Errorf("allowHost(%q): expected an error, got nil", host)
			continue
		}
		if !errors.Is(err, ErrForeignHost) {
			t.Errorf("allowHost(%q): expected errors.Is(err, ErrForeignHost), got %v", host, err)
		}
	}
}
