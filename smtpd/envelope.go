package smtpd

import (
	"bytes"
	"strings"
	"time"
)

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

func (e *Envelope) AddRecipient(rcpt MailAddress) {
	e.Recipients = append(e.Recipients, rcpt)
}

func (e *Envelope) AddReceivedHeader(serverHostname string) {
	var buf bytes.Buffer

	buf.WriteString("Received: from ")
	buf.WriteString(e.Client.HeloHost)
	buf.WriteString(" [")
	buf.WriteString(e.Client.Addr().String())
	buf.WriteString("]\r\n")
	buf.WriteString("\tby ")
	buf.WriteString(serverHostname)
	buf.WriteString(" (spamrake) with ")

	if e.Client.HeloType == "HELO" {
		buf.WriteString("SMTP")
	} else if e.Client.HeloType == "EHLO" {
		buf.WriteString("ESMTP")
	} else {
		panic("Unknown HeloType " + e.Client.HeloType)
	}
	buf.WriteString("\r\n")

	buf.WriteString("\tfor <")
	buf.WriteString(e.Recipients[0].Email())
	buf.WriteString(">; ")
	buf.WriteString(time.Now().Format(time.RFC1123Z))
	buf.WriteString("\r\n")

	buf.Write(e.Data)

	e.Data = buf.Bytes()
}
