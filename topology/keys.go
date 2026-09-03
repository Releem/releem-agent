package topology

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// MaxKeyLength bounds topology identities before they reach downstream stores.
const MaxKeyLength = 200

// CompositeKey returns a stable, namespaced identity for one or more members.
func CompositeKey(namespace string, identities []string) string {
	unique := make([]string, 0, len(identities))
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		identity = strings.TrimSpace(identity)
		if identity == "" {
			continue
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		unique = append(unique, identity)
	}
	sort.Strings(unique)

	identity := strings.Join(unique, "\x00")
	key := namespace + ":" + identity
	if len(key) <= MaxKeyLength {
		return key
	}

	namespace = boundedNamespace(namespace)
	key = namespace + ":" + identity
	if len(key) <= MaxKeyLength {
		return key
	}

	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s:sha256:%x", namespace, digest)
}

func boundedNamespace(namespace string) string {
	if len(namespace)+len(":sha256:")+sha256.Size*2 <= MaxKeyLength {
		return namespace
	}

	digest := sha256.Sum256([]byte(namespace))
	return fmt.Sprintf("sha256:%x", digest)
}
