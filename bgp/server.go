package bgp

import (
	"context"
	"fmt"
	"log"

	api "github.com/osrg/gobgp/v3/api"
	"github.com/osrg/gobgp/v3/pkg/server"
)

type Collector struct {
	server *server.BgpServer
}

func NewCollector() *Collector {
	return &Collector{
		server: server.NewBgpServer(),
	}
}

func (c *Collector) Start(ctx context.Context, asn uint32, routerID string) error {
	go c.server.Serve()

	if err := c.server.StartBgp(ctx, &api.StartBgpRequest{
		Global: &api.Global{
			Asn:        asn,
			RouterId:   routerID,
			ListenPort: 179,
		},
	}); err != nil {
		return fmt.Errorf("failed to start BGP: %w", err)
	}

	return nil
}

func (c *Collector) AddNeighbor(ctx context.Context, addr string, asn uint32, multihop bool) error {
	peer := &api.Peer{
		Conf: &api.PeerConf{
			NeighborAddress: addr,
			PeerAsn:         asn,
		},
		// Reject all exports: this is a route collector — we receive, never send.
		ApplyPolicy: &api.ApplyPolicy{
			ExportPolicy: &api.PolicyAssignment{
				DefaultAction: api.RouteAction_REJECT,
			},
		},
	}

	if multihop {
		peer.EbgpMultihop = &api.EbgpMultihop{
			Enabled:     true,
			MultihopTtl: 255,
		}
	}

	if err := c.server.AddPeer(ctx, &api.AddPeerRequest{
		Peer: peer,
	}); err != nil {
		return fmt.Errorf("failed to add neighbor %s: %w", addr, err)
	}

	return nil
}

func (c *Collector) DeleteNeighbor(ctx context.Context, addr string) error {
	if err := c.server.DeletePeer(ctx, &api.DeletePeerRequest{
		Address: addr,
	}); err != nil {
		return fmt.Errorf("failed to delete neighbor %s: %w", addr, err)
	}
	return nil
}

func (c *Collector) ListNeighbors(ctx context.Context) (map[string]uint32, error) {
	peers := make(map[string]uint32)
	err := c.server.ListPeer(ctx, &api.ListPeerRequest{}, func(peer *api.Peer) {
		peers[peer.Conf.NeighborAddress] = peer.Conf.PeerAsn
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list peers: %w", err)
	}
	return peers, nil
}

func (c *Collector) WatchEvents(ctx context.Context, handlePath func(*api.Path)) error {
	watchReq := &api.WatchEventRequest{
		Table: &api.WatchEventRequest_Table{
			Filters: []*api.WatchEventRequest_Table_Filter{
				{
					// ADJIN captures all per-peer path updates (route collector mode).
					// Use BEST instead if you only want the single best path per prefix.
					Type: api.WatchEventRequest_Table_Filter_ADJIN,
				},
			},
		},
	}

	err := c.server.WatchEvent(ctx, watchReq, func(resp *api.WatchEventResponse) {
		if tableEv := resp.GetTable(); tableEv != nil {
			for _, path := range tableEv.Paths {
				handlePath(path)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to watch events: %w", err)
	}

	return nil
}

// GetAllPathsForPrefix queries the adj-in RIB across all neighbors for a single prefix.
// This provides a live, comprehensive view of all received paths for that prefix.
func (c *Collector) GetAllPathsForPrefix(ctx context.Context, prefix string) ([]*api.Path, error) {
	neighbors, err := c.ListNeighbors(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list neighbors: %w", err)
	}

	var allPaths []*api.Path
	for neighborAddr := range neighbors {
		if err := c.server.ListPath(ctx, &api.ListPathRequest{
			TableType: api.TableType_ADJ_IN,
			Name:      neighborAddr,
			Prefixes:  []*api.TableLookupPrefix{{Prefix: prefix}},
		}, func(dest *api.Destination) {
			allPaths = append(allPaths, dest.Paths...)
		}); err != nil {
			log.Printf("ListPath adj-in for neighbor %s prefix %s: %v", neighborAddr, prefix, err)
		}
	}
	return allPaths, nil
}
