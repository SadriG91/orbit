package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/netip"
	"time"

	"github.com/tmc/go-iroh/iroh"
	"github.com/tmc/go-iroh/key"
	"github.com/tmc/go-iroh/netaddr"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const alpn = "orbit-spike/echo/1"

	srvKey, err := key.GenerateSecretKey()
	if err != nil {
		log.Fatal("genkey:", err)
	}

	server, err := iroh.Bind(ctx, iroh.WithSecretKey(srvKey), iroh.WithALPNs(alpn),
		iroh.WithBindAddr(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)))
	if err != nil {
		log.Fatal("bind server:", err)
	}
	defer server.Shutdown(ctx)
	fmt.Println("server bound, id =", server.ID())

	client, err := iroh.Bind(ctx, iroh.WithBindAddr(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)))
	if err != nil {
		log.Fatal("bind client:", err)
	}
	defer client.Shutdown(ctx)
	fmt.Println("client bound, id =", client.ID())

	go func() {
		conn, err := server.Accept(ctx)
		if err != nil {
			fmt.Println("server accept error:", err)
			return
		}
		s, err := conn.AcceptStream(ctx)
		if err != nil {
			fmt.Println("server accept stream error:", err)
			return
		}
		io.Copy(s, s)
		s.Close()
	}()

	addr := netaddr.NewEndpointAddr(server.ID()).WithIP(server.LocalAddr())
	conn, err := client.Connect(ctx, addr, alpn)
	if err != nil {
		log.Fatal("connect:", err)
	}
	defer conn.CloseWithError(0, "")

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		log.Fatal("open stream:", err)
	}

	msg := []byte("hello from orbit spike")
	if _, err := stream.Write(msg); err != nil {
		log.Fatal("write:", err)
	}
	stream.Close()

	got, err := io.ReadAll(stream)
	if err != nil {
		log.Fatal("read:", err)
	}
	fmt.Printf("echoed back: %q (match=%v)\n", got, string(got) == string(msg))

	stats := conn.Stats()
	fmt.Println("bytes sent:", stats.BytesSent, "bytes recv:", stats.BytesReceived)
}
