package mail

import (
	"context"
	"strings"
	"testing"
)

// An image part the HTML names by Content-ID is embedded, so it shows
// without a request.
func TestParseMessageEmbedsInlineImages(t *testing.T) {
	raw := strings.Join([]string{
		"From: Shop <news@shop.example>",
		"Subject: Pictures",
		"MIME-Version: 1.0",
		`Content-Type: multipart/related; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		`<p>Hello <img src="cid:logo@shop.example" alt="logo"></p>`,
		"--b",
		"Content-Type: image/png",
		"Content-ID: <logo@shop.example>",
		"Content-Transfer-Encoding: base64",
		"",
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
		"--b--",
	}, "\r\n")
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.BodyHTML, "cid:logo@shop.example") {
		t.Errorf("the inline image is still a reference: %q", m.BodyHTML)
	}
	if !strings.Contains(m.BodyHTML, "data:image/png;base64,") {
		t.Errorf("the image was not embedded: %q", m.BodyHTML)
	}
	if m.HasAttachments || len(m.Attachments) != 0 {
		t.Errorf("an inline image became an attachment: %+v", m.Attachments)
	}
}

func TestRemoteImages(t *testing.T) {
	m := Message{BodyHTML: `<p><img src="https://a.example/1.png"><img src="cid:x@y"><img src="http://b.example/2.jpg"><img src="https://a.example/1.png"></p>`}
	got := m.RemoteImages()
	want := []string{"https://a.example/1.png", "http://b.example/2.jpg"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("remote images = %q, want %q", got, want)
	}
	if got := (Message{BodyHTML: `<img src="data:image/png;base64,AA">`}).RemoteImages(); len(got) != 0 {
		t.Errorf("a data image is remote: %q", got)
	}
}

func TestRemoteImagePolicy(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	from := Address{Name: "Shop", Email: "News@Shop.Example"}
	policy, err := svc.RemoteImages(ctx)
	if err != nil || len(policy) != 0 {
		t.Fatalf("initial policy = %v, %v", policy, err)
	}
	if err := svc.SetRemoteImages(ctx, from, true); err != nil {
		t.Fatal(err)
	}
	policy, err = svc.RemoteImages(ctx)
	if err != nil || !policy["news@shop.example"] {
		t.Fatalf("policy = %v, %v", policy, err)
	}
	if err := svc.SetRemoteImages(ctx, from, false); err != nil {
		t.Fatal(err)
	}
	policy, _ = svc.RemoteImages(ctx)
	if policy["news@shop.example"] {
		t.Error("the sender stayed allowed")
	}
	if err := svc.SetRemoteImages(ctx, Address{}, true); err != nil {
		t.Errorf("an empty sender: %v", err)
	}
}
