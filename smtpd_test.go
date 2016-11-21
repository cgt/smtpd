package smtpd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/smtp"
	"net/textproto"
	"sync"
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

func TestPregreet(t *testing.T) {
	wg := new(sync.WaitGroup)
	wg.Add(1)
	logger := log.New(ioutil.Discard, "", 0)
	serverHostname := "server.invalid"
	s := Server{
		Addr:          "127.0.0.1:0",
		Hostname:      serverHostname,
		PregreetDelay: 5 * time.Second,
		Deliver: func(env *Envelope) error {
			if !env.Client.Pregreeted {
				t.Error("Server did not detect client pregreet")
			} else {
				t.Log("Server detected client pregreet")
			}
			wg.Done()
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

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	x := "HELO client.invalid\r\n" +
		"MAIL FROM:<bob@client.invalid>\r\n" +
		"RCPT TO:<joe@server.invalid>\r\n" +
		"DATA\r\n" +
		"The e-mail goes here.\r\n" +
		".\r\n" +
		"QUIT\r\n"
	_, err = io.WriteString(conn, x)
	if err != nil {
		t.Fatal(err)
	}

	for {
		br := bufio.NewReader(conn)
		str, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if str == "221 2.0.0 Bye\r\n" {
			break
		}
	}

	conn.Close()
	wg.Wait()
	cancel()
}

func TestNotPregreet(t *testing.T) {
	// This test necessarily requires the server to wait for a couple of seconds
	if testing.Short() {
		t.SkipNow()
	}

	wg := new(sync.WaitGroup)
	wg.Add(1)
	logger := log.New(ioutil.Discard, "", 0)
	serverHostname := "server.invalid"
	s := Server{
		Addr:          "127.0.0.1:0",
		Hostname:      serverHostname,
		PregreetDelay: 3 * time.Second,
		Deliver: func(env *Envelope) error {
			if env.Client.Pregreeted {
				t.Error("Server detected client pregreet")
			} else {
				t.Log("Server did not detect client pregreet")
			}
			wg.Done()
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
	if err != nil {
		t.Fatal(err)
	}

	wc, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(wc, "Mail goes here\r\n.\r\n")
	if err != nil {
		t.Fatal(err)
	}
	wc.Close()

	err = c.Quit()
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	cancel()
}
