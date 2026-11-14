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
		if n.Type == htmlnode.ElementNode {
			for _, attr := range n.Attr {
				for _, url := range remoteImageURLs(n.DataAtom, attr) {
					if !seen[url] {
						seen[url] = true
						out = append(out, url)
					}
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

// remoteImageURLs reads an attribute as the sanitizer does when it decides
// what an image may load: a picture's address, a candidate of its srcset,
// a video's poster or an element's background. The window must offer to
// allow exactly what the render would otherwise leave out.
func remoteImageURLs(tag atom.Atom, attr htmlnode.Attribute) []string {
	key := strings.ToLower(attr.Key)
	var raw []string
	switch key {
	case "src":
		if tag == atom.Img {
			raw = []string{attr.Val}
		}
	case "srcset", "imagesrcset":
		for _, candidate := range strings.Split(attr.Val, ",") {
			if fields := strings.Fields(candidate); len(fields) > 0 {
				raw = append(raw, fields[0])
			}
		}
	case "poster":
		if tag == atom.Video {
			raw = []string{attr.Val}
		}
	case "background":
		raw = []string{attr.Val}
	}
	var out []string
	for _, url := range raw {
		if isRemoteURL(url) {
			out = append(out, url)
		}
	}
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
