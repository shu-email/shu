package mail

import (
	"context"
	"encoding/json"
	"strings"

	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Remote images: the pictures an HTML message names on the network. They
// stay blocked until the user allows them for the message or for its
// sender. The policy is kept in the store, by sender address.

const settingRemoteImages = "mail.remote_images"

// RemoteImages lists the http and https image addresses an HTML body
// names, each once, in the order they appear.
func (m Message) RemoteImages() []string {
	if strings.TrimSpace(m.BodyHTML) == "" {
		return nil
	}
	doc, err := htmlnode.Parse(strings.NewReader(m.BodyHTML))
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	var walk func(*htmlnode.Node)
	walk = func(n *htmlnode.Node) {
		if n.Type == htmlnode.ElementNode && n.DataAtom == atom.Img {
			for _, attr := range n.Attr {
				if strings.EqualFold(attr.Key, "src") && isRemoteURL(attr.Val) && !seen[attr.Val] {
					seen[attr.Val] = true
					out = append(out, attr.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func isRemoteURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// RemoteImageKey is a sender's key in the image policy: its address
// without case or spaces.
func RemoteImageKey(from Address) string {
	return strings.ToLower(strings.TrimSpace(from.Email))
}

// RemoteImages reads the senders whose images the user allows, by key.
func (s *Service) RemoteImages(ctx context.Context) (map[string]bool, error) {
	raw, ok, err := s.store.Setting(ctx, settingRemoteImages)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return map[string]bool{}, err
	}
	out := map[string]bool{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]bool{}, nil
	}
	return out, nil
}

// SetRemoteImages allows or blocks the images of one sender for good, in
// the store alone. It never fetches anything.
func (s *Service) SetRemoteImages(ctx context.Context, from Address, on bool) error {
	key := RemoteImageKey(from)
	if key == "" {
		return nil
	}
	policy, err := s.RemoteImages(ctx)
	if err != nil {
		return err
	}
	if on {
		policy[key] = true
	} else {
		delete(policy, key)
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, settingRemoteImages, string(data))
}
