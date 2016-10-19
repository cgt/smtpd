package smtpd

import (
	"context"
	"net/textproto"
	"testing"
)

func TestHELO(t *testing.T) {
	serverHostname := "server.invalid"
	s := Server{
		Addr:     "127.0.0.1:0",
		Hostname: serverHostname,
	}

	ctx, cancel := context.WithCancel(context.Background())

	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		err = s.Serve(ctx, ln)
		if err != nil {
			t.Fatal(err)
		}
	}()

	c, err := textproto.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	code, msg, err := c.ReadCodeLine(220)
	if err != nil {
		t.Fatal(err)
	}

	id, err := c.Cmd("HELO client.invalid")
	if err != nil {
		t.Fatal(err)
	}
	c.StartResponse(id)
	code, msg, err = c.ReadResponse(250)
	if err != nil {
		t.Fatal(err)
	}
	c.EndResponse(id)
	if msg != "server.invalid" {
		t.Fatalf("unexpected HELO reply: code:%d msg:%s", code, msg)
	}
	cancel()
}
