package smtpd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	rcptToRE = regexp.MustCompile(`[Tt][Oo]:<(.+)>`)
	//mailFromRE = regexp.MustCompile(`(?i)^from:\s*<(.*?)>`)
	mailFromRE = regexp.MustCompile(`[Ff][Rr][Oo][Mm]:<(.*)>`)
)

// Server is an SMTP server.
type Server struct {
	Addr         string        // TCP address to listen on, ":25" if empty
	Hostname     string        // optional Hostname to announce; "" to use system hostname
	ReadTimeout  time.Duration // optional read timeout
	WriteTimeout time.Duration // optional write timeout

	// PregreetDelay is the amount of time to wait after sending the initial
	// "220-" line before sending the final "220 " line.
	PregreetDelay time.Duration

	// OnNewConnection, if non-nil, is called on new connections.
	// If it returns non-nil, the connection is closed.
	OnNewConnection func(c Connection) error

	// OnMailFrom, if non-nil, is called on MAIL FROM.
	// If the callback returns an SMTPError, the MAIL FROM address is not added
	// to the envelope, and the SMTPError is sent to the client as reply to
	// MAIL FROM.
	// If the callback returns a non-nil value that is not an SMTPError,
	// the address is also not added to the envelope, and the server replies
	// with "550 5.0.0 unacceptable sender" (permanent error).
	OnMailFrom func(c Connection, from MailAddress) error

	// OnRcptTo, if non-nil, is called on RCPT TO.
	// If it returns non-nil, the recipient is not rejected and not added
	// to the Envelope.
	// If the callback returns an SMTPError, the RCPT TO address is not added
	// to the envelope, and the SMTPError is sent to the client as reply to
	// MAIL FROM.
	// If the callback returns a non-nil value that is not an SMTPError,
	// the address is also not added to the envelope, and the server replies
	// with "550 5.0.0 unacceptable sender" (permanent error).
	OnRcptTo func(c Connection, rcpt MailAddress) error

	// Deliver is called when DATA is finished and the mail is ready to be
	// accepted. If it returns non-nil, the mail is rejected.
	Deliver func(env *Envelope) error

	// Log is used for logging within the Server.
	// If nil, logging is disabled.
	Log *log.Logger
}

// Connection is implemented by the SMTP library and provided to callers
// customizing their own Servers.
type Connection interface {
	Addr() net.Addr
}

func (srv *Server) hostname() string {
	if srv.Hostname != "" {
		return srv.Hostname
	}
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hostname
}

// ListenAndServe listens on the TCP network address srv.Addr and then
// calls Serve to handle requests on incoming connections.  If
// srv.Addr is blank, ":25" is used.
func (srv *Server) ListenAndServe(ctx context.Context) error {
	ln, err := srv.Listen()
	if err != nil {
		return err
	}
	return srv.Serve(ctx, ln)
}

// Listen listens on the TCP network address srv.Addr,
// but does not call Serve.
func (srv *Server) Listen() (net.Listener, error) {
	addr := srv.Addr
	if addr == "" {
		addr = ":25"
	}
	return net.Listen("tcp", addr)
}

// Serve handles incoming SMTP connections on the provided listener.
// Serve blocks until the context is cancelled and all connections
// have been gracefully shut down.
// Returns a non-nil error if a fatal accept error occurs.
func (srv *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv.Logf("Listening on %v", ln.Addr())
	defer ln.Close()
	conns := make(chan net.Conn)
	acceptErr := make(chan error)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					srv.Logf("Accept error: %v", err)
					continue
				}
				srv.Logf("Fatal accept error: %v", err)
				acceptErr <- err
				break
			}
			conns <- c
		}
	}()

	var (
		wg   sync.WaitGroup
		stop = false
	)
	for !stop {
		select {
		case <-ctx.Done():
			stop = true
		case <-acceptErr:
			stop = true
		case c := <-conns:
			wg.Add(1)
			go func() {
				srv.Logf("Accepted connection from %s", c.RemoteAddr())
				srv.newSession(c).serve(ctx)
				wg.Done()
			}()
		}
	}
	wg.Wait()
	return nil
}

// Logf calls Printf on srv.Log with the provided arguments
// if srv.Log is not nil.
func (srv *Server) Logf(format string, v ...interface{}) {
	if srv.Log != nil {
		srv.Log.Printf("smtpd: "+format, v)
	}
}

// TODO: flags on client (e.g., pregreeted)

type Client struct {
	HeloType   string
	HeloHost   string
	Pregreeted bool
	addr       net.Addr
}

func (c Client) Addr() net.Addr {
	return c.addr
}

type session struct {
	srv *Server
	rwc net.Conn
	br  *bufio.Reader
	bw  *bufio.Writer

	client Client
	env    *Envelope // current envelope, or nil
}

func (srv *Server) newSession(rwc net.Conn) *session {
	return &session{
		srv:    srv,
		rwc:    rwc,
		br:     bufio.NewReader(rwc),
		bw:     bufio.NewWriter(rwc),
		client: Client{addr: rwc.RemoteAddr()},
	}
}

func (s *session) errorf(format string, args ...interface{}) {
	s.srv.Logf("Client error: "+format, args...)
}

// Logf logs a message prefixed with the client IP address.
func (s *session) Logf(format string, v ...interface{}) {
	s.srv.Log.Printf("[%s] "+format, s.Addr(), v)
}

func (s *session) sendf(format string, args ...interface{}) {
	if s.srv.WriteTimeout != 0 {
		s.rwc.SetWriteDeadline(time.Now().Add(s.srv.WriteTimeout))
	}
	fmt.Fprintf(s.bw, format, args...)
	s.bw.Flush()
}

func (s *session) sendlinef(format string, args ...interface{}) {
	s.sendf(format+"\r\n", args...)
}

func (s *session) sendSMTPErrorOrLinef(err error, format string, args ...interface{}) {
	if se, ok := err.(SMTPError); ok {
		s.sendlinef("%s", se.Error())
		return
	}
	s.sendlinef(format, args...)
}

func (s *session) Addr() net.Addr {
	return s.rwc.RemoteAddr()
}

// pregreetCheck checks whether the client speaks before the full 220 greeting
// has been sent. If the client pregreets, returns a non-empty string
// containing the line the client sent. Otherwise returns empty string.
func (s *session) pregreetCheck() (line string) {
	s.sendlinef("220-Wait")

	wait := time.Tick(s.srv.PregreetDelay)
	var (
		buf  []byte
		stop = false
	)
	for !stop {
		select {
		case <-wait:
			stop = true
		default:
			s.rwc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			b, err := s.br.ReadByte()
			if err != nil {
				continue
			}
			buf = append(buf, b)
			if b == '\n' {
				stop = true
			}
		}
	}

	if len(buf) != 0 {
		line := string(buf)
		s.Logf("Client pregreeted with %#v", line)
		return line
	}
	return ""
}

func (s *session) serve(ctx context.Context) {
	defer s.rwc.Close()
	if onc := s.srv.OnNewConnection; onc != nil {
		if err := onc(s); err != nil {
			s.sendSMTPErrorOrLinef(err, "554 connection rejected")
			return
		}
	}

	var preline string
	if s.srv.PregreetDelay != 0 {
		preline = s.pregreetCheck()
		if preline != "" {
			s.client.Pregreeted = true
		}
	}

	s.sendlinef("220 %s ESMTP", s.srv.hostname())
	for {
		select {
		case <-ctx.Done():
			s.sendlinef("421 Server shutting down")
			return
		default:
		}

		var line cmdLine
		if preline == "" {
			if s.srv.ReadTimeout == 0 {
				s.rwc.SetReadDeadline(time.Time{})
			} else {
				s.rwc.SetReadDeadline(time.Now().Add(s.srv.ReadTimeout))
			}
			sl, err := s.br.ReadSlice('\n')
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				s.errorf("read error: %v", err)
				return
			}
			line = cmdLine(string(sl))
		} else {
			line = cmdLine(preline)
			preline = ""
		}
		if err := line.checkValid(); err != nil {
			s.sendlinef("500 %v", err)
			continue
		}

		switch line.Verb() {
		case "HELO":
			s.handleHELO(line.Arg())
		case "EHLO":
			s.handleEHLO(line.Arg())
		case "QUIT":
			s.sendlinef("221 2.0.0 Bye")
			return
		case "RSET":
			s.env = nil
			s.sendlinef("250 2.0.0 OK")
		case "NOOP":
			s.sendlinef("250 2.0.0 OK")
		case "MAIL":
			arg := line.Arg() // "From:<foo@bar.com>"
			m := mailFromRE.FindStringSubmatch(arg)
			if m == nil {
				s.Logf("invalid MAIL arg: %q", arg)
				s.sendlinef("501 5.1.7 Bad sender address syntax")
				continue
			}
			s.handleMailFrom(m[1])
		case "RCPT":
			s.handleRcpt(line)
		case "DATA":
			s.handleData()
		default:
			s.Logf("Client: %q, verhb: %q", line, line.Verb()) // TODO: ??
			s.sendlinef("502 5.5.2 Error: command not recognized")
		}
	}
}

func (s *session) handleHELO(host string) {
	s.client.HeloType = "HELO"
	s.client.HeloHost = host
	s.sendlinef("250 %s", s.srv.hostname())
}

func (s *session) handleEHLO(host string) {
	s.client.HeloType = "EHLO"
	s.client.HeloHost = host
	fmt.Fprintf(s.bw, "250-%s\r\n", s.srv.hostname())
	extensions := []string{}
	extensions = append(extensions, "250-PIPELINING",
		"250-SIZE 10240000",
		"250-ENHANCEDSTATUSCODES",
		"250-8BITMIME",
		"250 DSN")
	for _, ext := range extensions {
		fmt.Fprintf(s.bw, "%s\r\n", ext)
	}
	s.bw.Flush()
}

func (s *session) handleMailFrom(email string) {
	// TODO: 4.1.1.11.  If the server SMTP does not recognize or
	// cannot implement one or more of the parameters associated
	// with a particular MAIL FROM or RCPT TO command, it will return
	// code 555.

	if s.env != nil {
		s.sendlinef("503 5.5.1 Error: nested MAIL command")
		return
	}
	s.Logf("mail from: %q", email)

	cb := s.srv.OnMailFrom
	if cb != nil {
		if err := cb(s, MailAddress(email)); err != nil {
			s.sendSMTPErrorOrLinef(err, "550 5.0.0 unacceptable sender")
			s.Logf("rejected sender %s: %v", email, err)
			return
		}
	}

	s.env = &Envelope{Sender: MailAddress(email), Client: s.client}
	s.sendlinef("250 2.1.0 Ok")
}

func (s *session) handleRcpt(line cmdLine) {
	// TODO: 4.1.1.11.  If the server SMTP does not recognize or
	// cannot implement one or more of the parameters associated
	// with a particular MAIL FROM or RCPT TO command, it will return
	// code 555.

	if s.env == nil {
		s.sendlinef("503 5.5.1 Error: need MAIL command")
		return
	}
	arg := line.Arg() // "To:<foo@bar.com>"
	m := rcptToRE.FindStringSubmatch(arg)
	if m == nil {
		s.Logf("bad RCPT address: %q", arg)
		s.sendlinef("501 5.1.7 Bad sender address syntax")
		return
	}
	rcpt := MailAddress(m[1])

	cb := s.srv.OnRcptTo
	if cb != nil {
		if err := cb(s, rcpt); err != nil {
			s.sendSMTPErrorOrLinef(err, "550 5.0.0 unacceptable recipient")
			s.Logf("rejected recipient %s: %v", rcpt.Email(), err)
			return
		}
	}
	s.env.AddRecipient(rcpt)
	s.sendlinef("250 2.1.0 Ok")
}

func (s *session) handleData() {
	if s.env == nil {
		s.sendlinef("503 5.5.1 Error: need RCPT command")
		return
	}
	s.sendlinef("354 Go ahead")

	var buf []byte
	for {
		sl, err := s.br.ReadSlice('\n')
		if err != nil {
			s.errorf("read error: %v", err)
			return
		}
		if bytes.Equal(sl, []byte(".\r\n")) {
			break
		}
		if sl[0] == '.' {
			sl = sl[1:]
		}
		buf = append(buf, sl...)
	}
	s.env.Data = buf

	s.env.AddReceivedHeader(s.srv.hostname())
	err := s.srv.Deliver(s.env)
	if err != nil {
		s.Logf("delivery error: %v", err)
		s.sendSMTPErrorOrLinef(err, "450 4.3.0 Service unavailable")
	} else {
		s.sendlinef("250 2.0.0 Ok: queued")
	}
	s.env = nil
}

func (s *session) handleError(err error) {
	if se, ok := err.(SMTPError); ok {
		s.sendlinef("%s", se)
		return
	}
	s.Logf("Error: %s", err)
	s.env = nil
}

type cmdLine string

func (cl cmdLine) checkValid() error {
	if !strings.HasSuffix(string(cl), "\r\n") {
		return errors.New(`line doesn't end in \r\n`)
	}
	// Check for verbs defined not to have an argument
	// (RFC 5321 s4.1.1)
	switch cl.Verb() {
	case "RSET", "DATA", "QUIT":
		if cl.Arg() != "" {
			return errors.New("unexpected argument")
		}
	}
	return nil
}

func (cl cmdLine) Verb() string {
	s := string(cl)
	if idx := strings.Index(s, " "); idx != -1 {
		return strings.ToUpper(s[:idx])
	}
	return strings.ToUpper(s[:len(s)-2])
}

func (cl cmdLine) Arg() string {
	s := string(cl)
	if idx := strings.Index(s, " "); idx != -1 {
		return strings.TrimRightFunc(s[idx+1:len(s)-2], unicode.IsSpace)
	}
	return ""
}

func (cl cmdLine) String() string {
	return string(cl)
}

// SMTPReply is a string to be sent to a client as an SMTP reply.
// E.g. "550 5.7.1 IP address blacklisted".
// SMTPReply is meant to be used as a return value in the callback functions
// in Server (e.g. OnMailFrom) that return an error.
// Bad things will happen if an SMTPError is not an SMTP error reply.
type SMTPError string

func (e SMTPError) Error() string {
	return string(e)
}
