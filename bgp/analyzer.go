package bgp

import (
	"fmt"
	"strings"
	"time"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/apiutil"
	bgp "github.com/osrg/gobgp/v3/pkg/packet/bgp"
)

var Tier1ASNs = map[uint32]string{
	174:   "Cogent",
	1299:  "Arelion (Telia)",
	2914:  "NTT",
	3257:  "GTT",
	3356:  "Lumen (Level3)",
	3491:  "PCCW",
	5511:  "Orange",
	6453:  "Tata",
	6461:  "Zayo",
	6762:  "Telecom Italia",
	6830:  "Liberty Global",
	7018:  "AT&T",
	12956: "Telefonica",
}

type PrefixAnalysis struct {
	Prefix      string    `json:"prefix"`
	IsWithdraw  bool      `json:"is_withdraw,omitempty"`
	ASPath      []uint32  `json:"as_path"`
	NextHop     string    `json:"next_hop"`
	Communities []uint32  `json:"communities"`
	Origin      string    `json:"origin"`
	Neighbor    string    `json:"peer"`
	PeerASN     uint32    `json:"peer_asn"`
	ReceivedAt  time.Time `json:"received_at"`

	// Specific to bgp.tools logic
	OriginAS       uint32   `json:"origin_as"`
	DirectUpstream uint32   `json:"direct_upstream"`
	UpstreamChain  []uint32 `json:"upstream_chain"`
	Tier1Found     uint32   `json:"tier1,omitempty"`
}

func AnalyzePath(path *api.Path) (*PrefixAnalysis, error) {
	analysis := &PrefixAnalysis{
		IsWithdraw: path.IsWithdraw,
		Neighbor:   path.NeighborIp,
		ReceivedAt: time.Now(),
	}

	// NLRI
	if path.Nlri != nil {
		nlri, err := apiutil.UnmarshalNLRI(apiutil.ToRouteFamily(path.Family), path.Nlri)
		if err == nil {
			analysis.Prefix = nlri.String()
		} else {
			analysis.Prefix = "unknown"
		}
	}

	// Path Attributes
	attrs, err := apiutil.UnmarshalPathAttributes(path.Pattrs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal path attributes: %w", err)
	}

	for _, attr := range attrs {
		switch a := attr.(type) {
		case *bgp.PathAttributeAsPath:
			for _, param := range a.Value {
				if asPath, ok := param.(*bgp.As4PathParam); ok {
					analysis.ASPath = append(analysis.ASPath, asPath.AS...)
				} else if asPath, ok := param.(*bgp.AsPathParam); ok {
					for _, as := range asPath.AS {
						analysis.ASPath = append(analysis.ASPath, uint32(as))
					}
				}
			}
		case *bgp.PathAttributeNextHop:
			analysis.NextHop = a.Value.String()
		case *bgp.PathAttributeOrigin:
			switch a.Value {
			case 0:
				analysis.Origin = "IGP"
			case 1:
				analysis.Origin = "EGP"
			case 2:
				analysis.Origin = "INCOMPLETE"
			default:
				analysis.Origin = "UNKNOWN"
			}
		case *bgp.PathAttributeCommunities:
			analysis.Communities = a.Value
		}
	}

	// Logic from bgp.tools:
	// If a Tier 1 ASN appears in a path, any ASN between that Tier 1 and the originating ASN is classified as an upstream.
	if len(analysis.ASPath) > 0 {
		analysis.PeerASN = analysis.ASPath[0]
		analysis.OriginAS = analysis.ASPath[len(analysis.ASPath)-1]

		tier1Index := -1
		for i, asn := range analysis.ASPath {
			if _, ok := Tier1ASNs[asn]; ok {
				analysis.Tier1Found = asn
				tier1Index = i
				break
			}
		}

		if tier1Index != -1 {
			for j := tier1Index + 1; j < len(analysis.ASPath); j++ {
				analysis.UpstreamChain = append(analysis.UpstreamChain, analysis.ASPath[j-1])
			}
			if len(analysis.ASPath) > 1 {
				analysis.DirectUpstream = analysis.ASPath[len(analysis.ASPath)-2]
			}
		} else if len(analysis.ASPath) > 1 {
			analysis.DirectUpstream = analysis.ASPath[len(analysis.ASPath)-2]
		}
	}

	return analysis, nil
}

func (a *PrefixAnalysis) String() string {
	if a.IsWithdraw {
		return fmt.Sprintf("[ - ] %-18s | Neighbor: %s", a.Prefix, a.Neighbor)
	}

	asPathStr := strings.Trim(fmt.Sprint(a.ASPath), "[]")
	tier1Name := "None"
	if a.Tier1Found != 0 {
		tier1Name = fmt.Sprintf("AS%d (%s)", a.Tier1Found, Tier1ASNs[a.Tier1Found])
	}

	return fmt.Sprintf("[ + ] %-18s | Origin: AS%-6d | DirectUp: AS%-6d | Tier1: %-20s | Path: [%s]",
		a.Prefix, a.OriginAS, a.DirectUpstream, tier1Name, asPathStr)
}
