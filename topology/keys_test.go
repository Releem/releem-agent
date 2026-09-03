package topology

import (
	"strings"
	"testing"
)

func TestCompositeKeyBoundsOversizedNamespace(t *testing.T) {
	namespace := strings.Repeat("provider-namespace-", MaxKeyLength)
	key := CompositeKey(namespace, []string{"member-a"})

	if len(key) > MaxKeyLength {
		t.Fatalf("CompositeKey(oversized namespace, [member-a]) length = %d, want at most %d", len(key), MaxKeyLength)
	}
	if key != CompositeKey(namespace, []string{"member-a"}) {
		t.Fatalf("CompositeKey(oversized namespace, [member-a]) = %q, want a stable result", key)
	}
	if key == CompositeKey(namespace+"different", []string{"member-a"}) {
		t.Fatalf("CompositeKey(oversized namespace, [member-a]) = %q, want namespace-sensitive identity", key)
	}
}
