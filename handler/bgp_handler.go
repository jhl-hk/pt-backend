package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/jianyuelab/pt-backend/bgp"
	api "github.com/osrg/gobgp/v3/api"
)

type BGPHandler struct {
	collector *bgp.Collector
}

func NewBGPHandler(collector *bgp.Collector) *BGPHandler {
	return &BGPHandler{
		collector: collector,
	}
}

func (h *BGPHandler) StartMonitoring(ctx context.Context) {
	go func() {
		err := h.collector.WatchEvents(ctx, func(path *api.Path) {
			analysis, err := bgp.AnalyzePath(path)
			if err != nil {
				log.Printf("Analysis error: %v", err)
				return
			}

			// Here you would typically save to DB or push to a web socket
			fmt.Println(analysis.String())
		})
		if err != nil {
			log.Printf("Watch error: %v", err)
		}
	}()
}
