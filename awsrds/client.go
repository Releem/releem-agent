package awsrds

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// ParameterReader is the subset of the RDS API needed to inspect live
// instance and cluster parameter groups.
type ParameterReader interface {
	DescribeDBParameters(context.Context, *rds.DescribeDBParametersInput, ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error)
	DescribeDBClusterParameters(context.Context, *rds.DescribeDBClusterParametersInput, ...func(*rds.Options)) (*rds.DescribeDBClusterParametersOutput, error)
}

// Client is the subset of the RDS API used for topology discovery and
// parameter-group operations.
type Client interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	ParameterReader
	ModifyDBParameterGroup(context.Context, *rds.ModifyDBParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBParameterGroupOutput, error)
	ModifyDBClusterParameterGroup(context.Context, *rds.ModifyDBClusterParameterGroupInput, ...func(*rds.Options)) (*rds.ModifyDBClusterParameterGroupOutput, error)
}

var _ Client = (*rds.Client)(nil)
