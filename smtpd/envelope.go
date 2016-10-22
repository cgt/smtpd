package smtpd

import "strings"

type MailAddress string

func (a MailAddress) Email() string {
	return string(a)
}

func (a MailAddress) Hostname() string {
	e := string(a)
	if idx := strings.Index(e, "@"); idx != -1 {
		return strings.ToLower(e[idx+1:])
	}
	return ""
}

type Envelope struct {
	Client     Client
	Sender     MailAddress
	Recipients []MailAddress
	Data       []byte
}

// TODO: Add Received and Return-Path headers

func (e *Envelope) AddReceivedHeader(serverHostname string, client Client) {
	panic("not implemented")
}

func (e *Envelope) AddRecipient(rcpt MailAddress) {
	e.Recipients = append(e.Recipients, rcpt)
}
