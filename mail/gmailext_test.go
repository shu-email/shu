package mail

import "testing"

// TestGmailLabelValues checks the encoding X-GM-LABELS carries labels in,
// against the RFC's example and the values of Google's transcript.
func TestGmailLabelValues(t *testing.T) {
	tests := []struct {
		label, encoded string
	}{
		// The values of Google's example FETCH, where the one with a
		// space is a quoted string on the wire and its value here.
		{`\Inbox`, `\Inbox`},
		{`\Sent`, `\Sent`},
		{`Important`, `Important`},
		{`Muy Importante`, `Muy Importante`},
		{`foo`, `foo`},
		// "&" is the decoder's escape, so it cannot stand for itself.
		{`Tom & Jerry`, `Tom &- Jerry`},
		// RFC 3501 §5.1.3's own example, with ',' where base64 has '/'.
		{`~peter/mail/台北/日本語`, `~peter/mail/&U,BTFw-/&ZeVnLIqe-`},
		// A label a Chinese Gmail account is likely to have.
		{`所有邮件`, `&YkBnCZCuTvY-`},
		{``, ``},
	}
	for _, test := range tests {
		if got := gmailLabelEncode(test.label); got != test.encoded {
			t.Errorf("encode(%q) = %q, want %q", test.label, got, test.encoded)
		}
		got, err := gmailLabelDecode(test.encoded)
		if err != nil {
			t.Errorf("decode(%q): %v", test.encoded, err)
			continue
		}
		if got != test.label {
			t.Errorf("decode(%q) = %q, want %q", test.encoded, got, test.label)
		}
	}
}

// TestGmailLabelDecodeRefusesBrokenValues checks that a value the
// extension could not have written is an error, not a mangled label.
func TestGmailLabelDecodeRefusesBrokenValues(t *testing.T) {
	for _, value := range []string{
		`&BTFw`,  // no end to the run
		`&!!!!-`, // not base64
		`&BT-`,   // an odd number of bytes
	} {
		if got, err := gmailLabelDecode(value); err == nil {
			t.Errorf("decode(%q) = %q, want an error", value, got)
		}
	}
}
