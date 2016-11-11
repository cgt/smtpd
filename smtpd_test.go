package smtpd

import (
	"context"
	"errors"
	"io/ioutil"
	"log"
	"net/smtp"
	"net/textproto"
	"testing"
	"time"
)

func TestHELO(t *testing.T) {
	logger := log.New(ioutil.Discard, "", 0)
	serverHostname := "server.invalid"
	s := Server{
		Addr:        "127.0.0.1:0",
		Hostname:    serverHostname,
		ReadTimeout: 2 * time.Second,
		Log:         logger,
	}

	ctx, cancel := context.WithCancel(context.Background())

	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listening on %s", ln.Addr())
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

func TestRejectRecipient(t *testing.T) {
	logger := log.New(ioutil.Discard, "", 0)
	serverHostname := "server.invalid"
	s := Server{
		Addr:     "127.0.0.1:0",
		Hostname: serverHostname,
		OnMailFrom: func(c Connection, from MailAddress) error {
			return nil
		},
		OnRcptTo: func(c Connection, rcpt MailAddress) error {
			return errors.New("don't want mail for this address")
		},
		Log: logger,
	}

	ctx, cancel := context.WithCancel(context.Background())

	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listening on %s", ln.Addr())
	go func() {
		err = s.Serve(ctx, ln)
		if err != nil {
			t.Fatal(err)
		}
	}()

	c, err := smtp.Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	err = c.Hello("client.invalid")
	if err != nil {
		t.Fatal(err)
	}

	err = c.Mail("bob@client.invalid")
	if err != nil {
		t.Fatal(err)
	}

	err = c.Rcpt("anyone@server.invalid")
	if err == nil {
		t.Fatalf("expected SMTP error, got nil")
	}
	t.Log(err)

	c.Close()
	cancel()
}

func TestInvalidMailFromSpace(t *testing.T) {
	logger := log.New(ioutil.Discard, "", 0)
	serverHostname := "server.invalid"
	s := Server{
		Addr:     "127.0.0.1:0",
		Hostname: serverHostname,
		OnMailFrom: func(c Connection, from MailAddress) error {
			return nil
		},
		Log: logger,
	}

	ctx, cancel := context.WithCancel(context.Background())

	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listening on %s", ln.Addr())
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

	id, err = c.Cmd("MAIL FROM: <superfluous.space@example.net")
	if err != nil {
		t.Fatal(err)
	}

	c.StartResponse(id)
	code, msg, err = c.ReadResponse(501)
	if err != nil {
		t.Fatalf("expected response code 501, got %v: %v", code, msg)
	}

	cancel()
}
