package storage

import (
	"bytes"
	"testing"
)

func TestAbsChatID(t *testing.T) {
	tests := []struct {
		in, want int64
	}{
		{-1001234567890, 1001234567890},
		{1001234567890, 1001234567890},
		{0, 0},
		{-1, 1},
	}
	for _, tt := range tests {
		got := AbsChatID(tt.in)
		if got != tt.want {
			t.Errorf("AbsChatID(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestProfileKeyOrdering(t *testing.T) {
	k1 := ProfileKey(100, 200)
	k2 := ProfileKey(100, 300)
	k3 := ProfileKey(200, 100)

	if bytes.Compare(k1, k2) >= 0 {
		t.Fatal("k1 should sort before k2")
	}
	if bytes.Compare(k2, k3) >= 0 {
		t.Fatal("k2 should sort before k3")
	}
}

func TestChatIndexOrdering(t *testing.T) {
	i1 := ProfileChatIndex(100, 111)
	i2 := ProfileChatIndex(100, 222)
	i3 := ProfileChatIndex(200, 111)

	if bytes.Compare(i1, i2) >= 0 {
		t.Fatal("same chat, user 111 should sort before 222")
	}
	if bytes.Compare(i2, i3) >= 0 {
		t.Fatal("chat 100 should sort before chat 200")
	}
}

func TestPrefixScanWorks(t *testing.T) {
	prefix := ProfileChatPrefix(100)
	key := ProfileChatIndex(100, 111)

	if !bytes.HasPrefix(key, prefix) {
		t.Fatalf("key %q should have prefix %q", key, prefix)
	}

	otherKey := ProfileChatIndex(200, 111)
	if bytes.HasPrefix(otherKey, prefix) {
		t.Fatal("different chat should not match prefix")
	}
}

func TestWarnTargetPrefixScan(t *testing.T) {
	prefix := WarnTargetPrefix(100, 222)
	k1 := WarnTargetIndex(100, 222, "uuid-1")
	k2 := WarnTargetIndex(100, 222, "uuid-2")
	k3 := WarnTargetIndex(100, 333, "uuid-3")

	if !bytes.HasPrefix(k1, prefix) {
		t.Fatal("k1 should match prefix")
	}
	if !bytes.HasPrefix(k2, prefix) {
		t.Fatal("k2 should match prefix")
	}
	if bytes.HasPrefix(k3, prefix) {
		t.Fatal("different user should not match")
	}
}
