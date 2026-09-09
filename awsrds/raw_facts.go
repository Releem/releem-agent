package awsrds

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// AWSFacts is an allowlisted API observation. Optional bools retain unknown values.
// No role, group key, secret, tag, user name or unbounded SDK object is serialized.
type AWSFacts struct {
	Version       int
	Target        InstanceFacts
	Cluster       *ClusterFacts
	GlobalCluster *GlobalFacts
	Instances     []InstanceFacts
	Sources       map[string]string
}

type InstanceFacts struct {
	DBInstanceIdentifier                  string
	DBInstanceArn                         string
	DbiResourceId                         string
	DBInstanceClass                       string
	Engine                                string
	DBInstanceStatus                      string
	DBClusterIdentifier                   string
	Endpoint                              string
	Port                                  int32
	MultiAZ                               *bool
	ReadReplicaSourceDBInstanceIdentifier string
	ReadReplicaDBInstanceIdentifiers      []string
}

type ClusterMemberFacts struct {
	DBInstanceIdentifier          string
	IsClusterWriter               *bool
	PromotionTier                 *int32
	DBClusterParameterGroupStatus string
}

type ScalingFacts struct {
	MinCapacity           *float64
	MaxCapacity           *float64
	SecondsUntilAutoPause *int32
}

type ClusterFacts struct {
	DBClusterParameterGroup          string
	ServerlessV2ScalingConfiguration *ScalingFacts
	DBClusterIdentifier              string
	DBClusterArn                     string
	DbClusterResourceId              string
	Engine                           string
	EngineMode                       string
	Endpoint                         string
	ReaderEndpoint                   string
	GlobalClusterIdentifier          string
	GlobalWriteForwardingStatus      string
	DBClusterMembers                 []ClusterMemberFacts
}

type GlobalMemberFacts struct {
	DBClusterArn                string
	IsWriter                    *bool
	Readers                     []string
	SynchronizationStatus       string
	GlobalWriteForwardingStatus string
}

type GlobalFacts struct {
	GlobalClusterIdentifier string
	GlobalClusterArn        string
	GlobalClusterResourceId string
	GlobalClusterMembers    []GlobalMemberFacts
}

func instanceFacts(v types.DBInstance) InstanceFacts {
	f := InstanceFacts{DBInstanceIdentifier: aws.ToString(v.DBInstanceIdentifier), DBInstanceArn: aws.ToString(v.DBInstanceArn), DbiResourceId: aws.ToString(v.DbiResourceId), DBInstanceClass: aws.ToString(v.DBInstanceClass), Engine: aws.ToString(v.Engine), DBInstanceStatus: aws.ToString(v.DBInstanceStatus), DBClusterIdentifier: aws.ToString(v.DBClusterIdentifier), MultiAZ: v.MultiAZ, ReadReplicaSourceDBInstanceIdentifier: aws.ToString(v.ReadReplicaSourceDBInstanceIdentifier), ReadReplicaDBInstanceIdentifiers: append([]string{}, v.ReadReplicaDBInstanceIdentifiers...)}
	if v.Endpoint != nil {
		f.Endpoint = aws.ToString(v.Endpoint.Address)
		f.Port = aws.ToInt32(v.Endpoint.Port)
	}
	return f
}

func clusterFacts(v types.DBCluster) *ClusterFacts {
	f := &ClusterFacts{DBClusterIdentifier: aws.ToString(v.DBClusterIdentifier), DBClusterArn: aws.ToString(v.DBClusterArn), DbClusterResourceId: aws.ToString(v.DbClusterResourceId), Engine: aws.ToString(v.Engine), EngineMode: aws.ToString(v.EngineMode), Endpoint: aws.ToString(v.Endpoint), ReaderEndpoint: aws.ToString(v.ReaderEndpoint), GlobalClusterIdentifier: aws.ToString(v.GlobalClusterIdentifier), GlobalWriteForwardingStatus: string(v.GlobalWriteForwardingStatus), DBClusterMembers: []ClusterMemberFacts{}}
	f.DBClusterParameterGroup = aws.ToString(v.DBClusterParameterGroup)
	if v.ServerlessV2ScalingConfiguration != nil {
		c := v.ServerlessV2ScalingConfiguration
		f.ServerlessV2ScalingConfiguration = &ScalingFacts{MinCapacity: c.MinCapacity, MaxCapacity: c.MaxCapacity, SecondsUntilAutoPause: c.SecondsUntilAutoPause}
	}
	for _, m := range v.DBClusterMembers {
		f.DBClusterMembers = append(f.DBClusterMembers, ClusterMemberFacts{aws.ToString(m.DBInstanceIdentifier), m.IsClusterWriter, m.PromotionTier, aws.ToString(m.DBClusterParameterGroupStatus)})
	}
	return f
}

func globalFacts(v types.GlobalCluster) *GlobalFacts {
	f := &GlobalFacts{GlobalClusterIdentifier: aws.ToString(v.GlobalClusterIdentifier), GlobalClusterArn: aws.ToString(v.GlobalClusterArn), GlobalClusterResourceId: aws.ToString(v.GlobalClusterResourceId), GlobalClusterMembers: []GlobalMemberFacts{}}
	for _, m := range v.GlobalClusterMembers {
		f.GlobalClusterMembers = append(f.GlobalClusterMembers, GlobalMemberFacts{aws.ToString(m.DBClusterArn), m.IsWriter, append([]string{}, m.Readers...), string(m.SynchronizationStatus), string(m.GlobalWriteForwardingStatus)})
	}
	return f
}

// collectAWSFacts resolves only explicitly referenced API objects. Membership
// interpretation belongs to Platform; failed optional lookups cannot stop monitoring.
func collectAWSFacts(ctx context.Context, client Client, target types.DBInstance, metadata Metadata) Metadata {
	f := &AWSFacts{Version: 1, Target: instanceFacts(target), Instances: []InstanceFacts{}, Sources: map[string]string{"Target": "ok", "Cluster": "unsupported", "GlobalCluster": "unsupported", "Peers": "unsupported", "Source": "unsupported", "Children": "unsupported"}}
	metadata.TopologyFacts = f
	collect := func(id, source string) {
		v, err := describeDBInstanceInRegion(ctx, client, id, metadata.Region)
		if err != nil {
			f.Sources[source] = "error"
			metadata.TopologyIncomplete = true
			return
		}
		f.Instances = append(f.Instances, instanceFacts(v))
	}
	if id := aws.ToString(target.DBClusterIdentifier); id != "" {
		cluster, err := describeDBClusterInRegion(ctx, client, id, metadata.Region)
		if err != nil {
			f.Sources["Cluster"] = "error"
			f.Sources["GlobalCluster"] = "error"
			f.Sources["Peers"] = "error"
			metadata.TopologyIncomplete = true
			return metadata
		}
		f.Cluster = clusterFacts(cluster)
		f.Sources["Cluster"] = "ok"
		f.Sources["Peers"] = "ok"
		// These fields are consumed by existing host monitoring, independently of topology.
		_ = populateClusterMetadata(&metadata, cluster, false)
		for _, member := range cluster.DBClusterMembers {
			id := aws.ToString(member.DBInstanceIdentifier)
			if strings.EqualFold(id, metadata.DBInstanceIdentifier) {
				metadata.IsClusterWriter = aws.ToBool(member.IsClusterWriter)
				metadata.DBClusterParameterGroupStatus = aws.ToString(member.DBClusterParameterGroupStatus)
				metadata.PromotionTier = aws.ToInt32(member.PromotionTier)
				continue
			}
			collect(id, "Peers")
		}
		if id := aws.ToString(cluster.GlobalClusterIdentifier); id != "" {
			metadata.GlobalClusterIdentifier = id
			global, err := describeGlobalClusterInRegion(ctx, client, id, metadata.Region)
			if err != nil {
				f.Sources["GlobalCluster"] = "error"
				metadata.TopologyIncomplete = true
			} else {
				f.GlobalCluster = globalFacts(global)
				f.Sources["GlobalCluster"] = "ok"
			}
		}
		return metadata
	}
	if id := aws.ToString(target.ReadReplicaSourceDBInstanceIdentifier); id != "" {
		f.Sources["Source"] = "ok"
		collect(id, "Source")
	}
	if len(target.ReadReplicaDBInstanceIdentifiers) > 0 {
		f.Sources["Children"] = "ok"
		for _, id := range target.ReadReplicaDBInstanceIdentifiers {
			collect(id, "Children")
		}
	}
	return metadata
}

func (f *AWSFacts) clone() *AWSFacts {
	if f == nil {
		return nil
	}
	// The DTO has only JSON-compatible typed allowlisted fields.
	data, _ := json.Marshal(f)
	var result AWSFacts
	_ = json.Unmarshal(data, &result)
	return &result
}
