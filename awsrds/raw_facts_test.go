package awsrds

import (
	"context"
	"errors"
	"testing"
)

func TestOptionalClusterFailurePreservesTarget(t *testing.T) {
	fixture := loadDiscoveryFixture(t, "aurora_global_primary.json")
	client := fakeClientFromFixture(fixture)
	client.clusterPages = nil
	client.clustersErr = errors.New("AccessDenied")
	got, err := DiscoverInstance(context.Background(), client, fixture.TargetIdentifier)
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint == "" || !got.TopologyIncomplete {
		t.Fatal("lost target or reported complete")
	}
}
