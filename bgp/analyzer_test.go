package bgp

import (
	"testing"

	api "github.com/osrg/gobgp/v3/api"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestAnalyzePath(t *testing.T) {
	// Create a mock path
	nlri, _ := anypb.New(&api.IPAddressPrefix{
		Prefix:    "192.168.1.0",
		PrefixLen: 24,
	})

	origin, _ := anypb.New(&api.OriginAttribute{
		Origin: 0,
	})

	asPath, _ := anypb.New(&api.AsPathAttribute{
		Segments: []*api.AsSegment{
			{
				Type:    2, // SEQUENCE
				Numbers: []uint32{65001, 174, 65002},
			},
		},
	})

	path := &api.Path{
		Family: &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: []*anypb.Any{origin, asPath},
	}

	analysis, err := AnalyzePath(path)
	if err != nil {
		t.Fatalf("AnalyzePath failed: %v", err)
	}

	if analysis.Prefix != "192.168.1.0/24" {
		t.Errorf("expected prefix 192.168.1.0/24, got %s", analysis.Prefix)
	}

	if analysis.OriginAS != 65002 {
		t.Errorf("expected Origin AS65002, got AS%d", analysis.OriginAS)
	}

	if analysis.DirectUpstream != 174 {
		t.Errorf("expected DirectUpstream AS174, got AS%d", analysis.DirectUpstream)
	}

	if analysis.Tier1Found != 174 {
		t.Errorf("expected Tier1Found 174, got %d", analysis.Tier1Found)
	}

	if len(analysis.UpstreamChain) != 1 || analysis.UpstreamChain[0] != 174 {
		t.Errorf("expected UpstreamChain [174], got %v", analysis.UpstreamChain)
	}

	if analysis.Origin != "IGP" {
		t.Errorf("expected Origin IGP, got %s", analysis.Origin)
	}
}
