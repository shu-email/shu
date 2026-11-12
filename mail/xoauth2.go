package mail

import "github.com/emersion/go-sasl"

// xoauth2Mechanism is the SASL mechanism Gmail and Outlook take an OAuth
// access token by (https://developers.google.com/gmail/imap/xoauth2-protocol).
const xoauth2Mechanism = "XOAUTH2"

// xoauth2Client is the client side of XOAUTH2, for IMAP's AUTHENTICATE:
// the user and the access token in one message. A server that refuses
// the token answers it with a challenge saying why, in JSON, and ends the
// exchange with NO once the client has answered that with an empty
// response, as the mechanism requires. SMTP has go-mail's.
type xoauth2Client struct{ user, token string }

var _ sasl.Client = (*xoauth2Client)(nil)

func (x *xoauth2Client) Start() (string, []byte, error) {
	return xoauth2Mechanism, []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

func (x *xoauth2Client) Next([]byte) ([]byte, error) { return []byte{}, nil }
