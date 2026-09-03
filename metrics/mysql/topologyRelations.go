package mysql

import (
	"sort"
	"strings"

	"github.com/Releem/mysqlconfigurer/models"
	agenttopology "github.com/Releem/mysqlconfigurer/topology"
)

const maxTopologyKeyLength = agenttopology.MaxKeyLength

type topologyRelationIdentity struct {
	typeName  string
	groupKey  string
	memberKey string
}

// CompositeTopologyKey keeps the MySQL collector API while sharing the key
// contract with provider topology discovery.
func CompositeTopologyKey(namespace string, identities []string) string {
	return agenttopology.CompositeKey(namespace, identities)
}

// NormalizeTopologyRelations returns canonical, complete relation entries.
func NormalizeTopologyRelations(relations []models.MetricGroupValue) []models.MetricGroupValue {
	normalized := make([]models.MetricGroupValue, 0, len(relations))
	seen := make(map[topologyRelationIdentity]struct{}, len(relations))
	for _, relation := range relations {
		canonical := cloneTopologyRelation(relation)
		canonical["Type"] = strings.TrimSpace(stringValue(canonical["Type"]))
		canonical["GroupKey"] = strings.TrimSpace(stringValue(canonical["GroupKey"]))
		canonical["MemberKey"] = strings.TrimSpace(stringValue(canonical["MemberKey"]))

		identity := topologyRelationIdentity{
			typeName:  canonical["Type"].(string),
			groupKey:  canonical["GroupKey"].(string),
			memberKey: canonical["MemberKey"].(string),
		}
		if identity.typeName == "" || identity.groupKey == "" || identity.memberKey == "" {
			continue
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}

		normalizeTopologyRelationBooleans(canonical)
		normalizeTopologyRelationIntegers(canonical)
		normalized = append(normalized, canonical)
	}

	sort.Slice(normalized, func(left, right int) bool {
		leftIdentity := topologyRelationIdentity{
			typeName:  normalized[left]["Type"].(string),
			groupKey:  normalized[left]["GroupKey"].(string),
			memberKey: normalized[left]["MemberKey"].(string),
		}
		rightIdentity := topologyRelationIdentity{
			typeName:  normalized[right]["Type"].(string),
			groupKey:  normalized[right]["GroupKey"].(string),
			memberKey: normalized[right]["MemberKey"].(string),
		}
		if leftIdentity.typeName != rightIdentity.typeName {
			return leftIdentity.typeName < rightIdentity.typeName
		}
		if leftIdentity.groupKey != rightIdentity.groupKey {
			return leftIdentity.groupKey < rightIdentity.groupKey
		}
		return leftIdentity.memberKey < rightIdentity.memberKey
	})

	return normalized
}

func cloneTopologyRelation(input models.MetricGroupValue) models.MetricGroupValue {
	output := make(models.MetricGroupValue, len(input))
	for key, value := range input {
		output[key] = cloneTopologyRelationValue(value)
	}
	return output
}

func cloneTopologyRelationValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case models.MetricGroupValue:
		return cloneTopologyRelation(typed)
	case map[string]interface{}:
		output := make(map[string]interface{}, len(typed))
		for key, nestedValue := range typed {
			output[key] = cloneTopologyRelationValue(nestedValue)
		}
		return output
	case []models.MetricGroupValue:
		output := make([]models.MetricGroupValue, len(typed))
		for index, relation := range typed {
			output[index] = cloneTopologyRelation(relation)
		}
		return output
	case []map[string]interface{}:
		output := make([]map[string]interface{}, len(typed))
		for index, relation := range typed {
			output[index] = cloneTopologyRelationValue(relation).(map[string]interface{})
		}
		return output
	case []interface{}:
		output := make([]interface{}, len(typed))
		for index, nestedValue := range typed {
			output[index] = cloneTopologyRelationValue(nestedValue)
		}
		return output
	default:
		return value
	}
}

func normalizeTopologyRelationBooleans(relation models.MetricGroupValue) {
	for _, key := range []string{"IsWriter", "IsReader", "ReadOnly", "SuperReadOnly"} {
		value, ok := relation[key]
		if ok {
			relation[key] = truthy(stringValue(value))
		}
	}
}

func normalizeTopologyRelationIntegers(relation models.MetricGroupValue) {
	for _, key := range []string{"MemberPort", "PrimaryPort", "ReplicationLagSeconds"} {
		value, ok := relation[key]
		if !ok {
			continue
		}
		integer, valid := optionalInt64(stringValue(value))
		relation[key] = nullableInt64(integer, valid)
	}
}

// AttachTopologyRelations publishes the canonical complete snapshot and its
// one-release Facts compatibility mirror.
func AttachTopologyRelations(topology models.MetricGroupValue, relations []models.MetricGroupValue, complete bool) {
	normalized := NormalizeTopologyRelations(relations)
	facts, ok := topology["Facts"].(models.MetricGroupValue)
	if ok {
		facts = cloneMetricGroup(facts)
	} else {
		facts = models.MetricGroupValue{}
	}
	facts["Relations"] = normalized
	topology["Facts"] = facts

	if complete {
		topology["Relations"] = normalized
		return
	}
	delete(topology, "Relations")
}
