// Command perf-app is the app behind Roomr's benchmarks (perf/README.md). It serves
// session.v1.Session on 127.0.0.1:50071 (roomr.yaml's sessionEndpoint), answers every call with
// an L16/16000 RTP endpoint and echoes each packet back to the node, so neither leg goes silent
// and trips mediaTimeout. It is not under test: it is written in Go so that at the top of the
// invite-answer ramp it stays far from its cores' limit and the limit found is Roomr's.
package main

import (
	"log"
	"net"
	"time"

	sessionv1 "github.com/fonoster/roomr/api/session/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

const host = "127.0.0.1"

type app struct {
	sessionv1.UnimplementedSessionServer
}

func (app) Connect(stream sessionv1.Session_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil || first.GetStart() == nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(host)})
	if err != nil {
		return err
	}
	defer conn.Close()
	go echo(conn)

	media := &sessionv1.MediaEndpoint{Ip: host, Port: uint32(conn.LocalAddr().(*net.UDPAddr).Port), Codec: "L16/16000"}
	msg := &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Answer{Answer: &sessionv1.Answer{Media: media}}}
	if first.GetStart().GetDirection() == sessionv1.Start_OUTBOUND {
		msg = &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Ready{Ready: &sessionv1.Ready{Media: media}}}
	}
	if err := stream.Send(msg); err != nil {
		return err
	}
	for {
		m, err := stream.Recv()
		if err != nil || m.GetEvent().GetEnded() != nil {
			return nil
		}
	}
}

// echo sends every packet back to where it came from until the socket is closed.
func echo(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = conn.WriteToUDP(buf[:n], from)
	}
}

func main() {
	lis, err := net.Listen("tcp", host+":50071")
	if err != nil {
		log.Fatal(err)
	}
	// The node pings each control stream every 10 s.
	srv := grpc.NewServer(grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}))
	sessionv1.RegisterSessionServer(srv, app{})
	log.Printf("perf-app: session endpoint on %s", lis.Addr())
	log.Fatal(srv.Serve(lis))
}
