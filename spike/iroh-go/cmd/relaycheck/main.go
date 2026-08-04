package main

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/tmc/go-iroh/iroh"
	"github.com/tmc/go-iroh/relay"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ep, err := iroh.Bind(ctx, iroh.WithRelayMode(relay.ModeStaging()),
		iroh.WithBindAddr(netip.AddrPortFrom(netip.MustParseAddr("0.0.0.0"), 0)))
	if err != nil {
		fmt.Println("bind:", err)
		return
	}
	defer ep.Shutdown(ctx)

	if err := ep.Online(ctx); err != nil {
		fmt.Println("online (relay reachability) FAILED:", err)
		return
	}
	fmt.Println("online! relay URLs:", ep.Addr().RelayURLs())
}
