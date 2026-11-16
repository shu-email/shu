package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A sender's icon, the picture the reading pane shows in place of the
// initials of a name (avatars.go). It is the domain's own favicon, so a
// sender at vercel.com is shown Vercel's mark and one at github.com
// GitHub's. Only the domain leaves the device, never the address, and
// the user can turn the whole thing off (Settings.SenderIcons).
//
// What a domain's lookup found is kept: the bytes in the cache directory,
// one file per content hash, and the lookup in the store, so that a domain
// is asked for once and a domain that has none is asked again only after
// logoRetry. A lookup that could not reach any source records nothing, so
// that going offline does not cost a month of initials.

// StoredIcon is what the cache keeps of a domain's icon: the hash of the
// bytes in the cache directory, and whether there are any.
type StoredIcon struct {
	Domain      string
	ContentHash string
	Found       bool
	CheckedAt   time.Time
}

const (
	// logoTimeout bounds one domain's lookup, every source included.
	logoTimeout = 8 * time.Second
	// logoRequestTimeout bounds one request of it.
	logoRequestTimeout = 3 * time.Second
	// logoMaxRedirects is how many redirects one request may follow.
	logoMaxRedirects = 2
	// logoMaxBody bounds what one source may answer with.
	logoMaxBody = 128 << 10
	// logoRetry is how long a domain a lookup found nothing for waits
	// before it is asked again: a site that gains an icon meanwhile is
	// found late, not never.
	logoRetry = 30 * 24 * time.Hour
	// logoMaxSide is the largest icon that is kept; a bigger one is a
	// picture, not a mark.
	logoMaxSide = 1024
	// senderIconCacheDir is the directory of the icon files, one file per
	// content hash.
	senderIconCacheDir = "sender-icons"
)

// logoSources are the endpoints a domain's icon is looked for at, in the
// order they are tried. The first two are the domain's own hosts, which
// already see the mail they send. The last is a public icon service,
// which learns the domain and nothing else; it costs one request per
// domain that has neither of the first two, once.
var logoSources = []string{
	"https://%s/apple-touch-icon.png",
	"https://%s/favicon.ico",
	"https://icons.duckduckgo.com/ip3/%s.ico",
	"https://www.google.com/s2/favicons?sz=64&domain=%s",
}

// publicMailDomains are the domains of the mail services themselves: their
// icon is the provider's, not the sender's, and showing it says nothing
// about who wrote. A sender there keeps the initials of its name.
var publicMailDomains = map[string]bool{
	"126.com": true, "163.com": true, "21cn.com": true, "aol.com": true,
	"aliyun.com": true, "daum.net": true, "fastmail.com": true, "foxmail.com": true,
	"free.fr": true, "gmail.com": true, "gmx.de": true, "gmx.net": true,
	"googlemail.com": true, "hey.com": true, "hotmail.com": true, "hushmail.com": true,
	"icloud.com": true, "live.com": true, "mac.com": true, "mailbox.org": true,
	"mail.ru": true, "me.com": true, "msn.com": true, "naver.com": true,
	"onet.pl": true, "orange.fr": true, "outlook.com": true, "posteo.de": true,
	"proton.me": true, "protonmail.com": true, "pm.me": true, "qq.com": true,
	"rambler.ru": true, "riseup.net": true, "seznam.cz": true, "sina.com": true,
	"sohu.com": true, "t-online.de": true, "tuta.io": true, "tutamail.com": true,
	"tutanota.com": true, "web.de": true, "wp.pl": true, "yahoo.com": true,
	"yandex.ru": true, "yeah.net": true, "zoho.com": true,
}

// LogoDomain names the domain a sender's icon is looked up by, and reports
// whether one is to be looked for: a public mailbox's icon is the
// provider's, not the sender's, and an address that names no domain has
// none.
func LogoDomain(addr Address) (string, bool) {
	email := strings.TrimSpace(addr.Email)
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return "", false
	}
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(email[at+1:]), "."))
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, " \t/@:[]") || !strings.Contains(domain, ".") {
		return "", false
	}
	if net.ParseIP(domain) != nil || publicMailDomains[domain] {
		return "", false
	}
	return domain, true
}

// SenderIcon returns the icon kept for the sender's domain, looking it up
// when this device has not before, or nil when there is none. It is
// bounded in time and safe to call from a background job; the window
// decides when to ask, and asks once per domain.
func (s *Service) SenderIcon(ctx context.Context, addr Address) ([]byte, error) {
	domain, ok := LogoDomain(addr)
	if !ok || s.cacheDir == "" {
		return nil, nil
	}
	// One lookup at a time: a list that shows many new senders asks for
	// several, and the sources are shared.
	s.logoMu.Lock()
	defer s.logoMu.Unlock()

	stored, has, err := s.store.SenderIcon(ctx, domain)
	if err != nil {
		return nil, err
	}
	if has && stored.Found {
		if data, err := s.readIcon(stored.ContentHash); err == nil {
			return data, nil
		}
		// The file is gone though the row says it was there: a sweep or
		// the user removed it, and the domain is asked again.
	}
	if has && !stored.Found && s.now().Sub(stored.CheckedAt) < logoRetry {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.logoTimeout)
	defer cancel()
	data, answered, err := s.fetchIcon(ctx, domain)
	if data == nil {
		if !answered {
			// Nothing answered: a month of initials is too long for a
			// network that may come back in a minute.
			return nil, err
		}
		if saveErr := s.store.SaveSenderIcon(ctx, StoredIcon{Domain: domain, CheckedAt: s.now()}); saveErr != nil {
			return nil, saveErr
		}
		return nil, nil
	}
	hash := hashIcon(data)
	if err := s.writeIcon(hash, data); err != nil {
		return nil, err
	}
	if err := s.store.SaveSenderIcon(ctx, StoredIcon{
		Domain: domain, ContentHash: hash, Found: true, CheckedAt: s.now(),
	}); err != nil {
		return nil, err
	}
	return data, nil
}

// fetchIcon looks the domain's icon up at the sources in order, and
// reports whether any of them answered: one that answered with nothing is
// worth recording, one that could not be reached is not.
func (s *Service) fetchIcon(ctx context.Context, domain string) ([]byte, bool, error) {
	client, err := s.logoHTTPClient(ctx)
	if err != nil {
		return nil, false, err
	}
	answered := false
	var last error
	for _, source := range logoSources {
		u := fmt.Sprintf(source, domain)
		data, ok, err := s.logoFetch(ctx, client, u)
		if ok {
			return data, true, nil
		}
		if err == nil {
			continue
		}
		// A status or a body that is no image is an answer; a request
		// that never arrived is not.
		if iconAnswered(err) {
			answered = true
		}
		last = err
	}
	if answered {
		return nil, true, nil
	}
	return nil, false, last
}

// errIconStatus is a source that answered with a status other than 200,
// and errIconBody one that answered with no image.
var (
	errIconStatus = errors.New("no icon")
	errIconBody   = errors.New("not an image")
)

// iconAnswered reports whether err is a source's answer rather than a
// request that could not be made.
func iconAnswered(err error) bool {
	return errors.Is(err, errIconStatus) || errors.Is(err, errIconBody)
}

// logoFetch reads one source, bounded in time and size, and returns the
// icon as PNG bytes.
func (s *Service) logoFetch(ctx context.Context, client *http.Client, u string) ([]byte, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.logoRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "Shu")
	req.Header.Set("Accept", "image/png, image/jpeg, image/webp, image/gif, image/x-icon, image/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("%w: %s", errIconStatus, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, logoMaxBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 || len(data) > logoMaxBody {
		return nil, false, errIconBody
	}
	png, err := iconPNG(data)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errIconBody, err)
	}
	return png, true, nil
}

// logoHTTPClient is the client the icons are read with: the settings'
// proxy and certificate authority, a cap on redirects, and no exception
// for a URL that is not https.
func (s *Service) logoHTTPClient(ctx context.Context) (*http.Client, error) {
	base, err := s.logoClient(ctx)
	if err != nil {
		return nil, err
	}
	guard := func(req *http.Request, via []*http.Request) error {
		if len(via) >= logoMaxRedirects {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to a URL that is not https")
		}
		return nil
	}
	policy := guard
	if base.CheckRedirect != nil {
		inner := base.CheckRedirect
		policy = func(req *http.Request, via []*http.Request) error {
			if err := inner(req, via); err != nil {
				return err
			}
			return guard(req, via)
		}
	}
	return &http.Client{Transport: base.Transport, Timeout: base.Timeout, CheckRedirect: policy}, nil
}

// readIcon reads an icon file of the cache.
func (s *Service) readIcon(hash string) ([]byte, error) {
	if hash == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.cacheDir, senderIconCacheDir, hash))
}

// writeIcon keeps an icon's bytes, one file per content hash.
func (s *Service) writeIcon(hash string, data []byte) error {
	dir := filepath.Join(s.cacheDir, senderIconCacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, hash), data, 0o600)
}

// hashIcon names an icon's file by its bytes.
func hashIcon(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// iconPNG returns the icon's bytes as a PNG: one that already is an image
// the window can draw is taken as it is, and an ICO, which it cannot, is
// decoded and encoded again.
func iconPNG(data []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(data, pngMagic):
		return data, nil
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")),
		bytes.HasPrefix(data, []byte("GIF87a")),
		bytes.HasPrefix(data, []byte("GIF89a")):
		return data, nil
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return data, nil
	case len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0:
		return decodeICO(data)
	}
	return nil, errors.New("unknown image format")
}

// decodeICO decodes the largest picture of a Windows icon, as a PNG. A
// site's favicon.ico is often the whole mark at 32 pixels, and the newer
// entries in the file are PNGs already.
func decodeICO(data []byte) ([]byte, error) {
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count <= 0 || len(data) < 6+16*count {
		return nil, errors.New("truncated icon directory")
	}
	best, area := -1, 0
	for i := 0; i < count; i++ {
		e := data[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if w*h > area {
			best, area = i, w*h
		}
	}
	e := data[6+16*best:]
	size := int(binary.LittleEndian.Uint32(e[8:12]))
	off := int(binary.LittleEndian.Uint32(e[12:16]))
	if size <= 0 || off < 0 || off+size > len(data) {
		return nil, errors.New("icon outside the file")
	}
	payload := data[off : off+size]
	// The entry of a modern icon is a PNG, and the window draws it as
	// it is.
	if bytes.HasPrefix(payload, pngMagic) {
		return payload, nil
	}
	img, err := decodeDIB(payload)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeDIB decodes the bitmap an older icon entry holds: a header, an
// optional palette and rows that run bottom-up, followed by a mask that
// says which pixels are see-through.
func decodeDIB(b []byte) (image.Image, error) {
	if len(b) < 40 {
		return nil, errors.New("truncated icon bitmap")
	}
	headerSize := int(binary.LittleEndian.Uint32(b[0:4]))
	width := int(int32(binary.LittleEndian.Uint32(b[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(b[8:12]))) / 2
	bits := int(binary.LittleEndian.Uint16(b[14:16]))
	compression := binary.LittleEndian.Uint32(b[16:20])
	if compression != 0 && compression != 3 {
		return nil, fmt.Errorf("icon compression %d", compression)
	}
	if headerSize < 40 || headerSize > len(b) {
		return nil, errors.New("icon header outside the bitmap")
	}
	if width <= 0 || height <= 0 || width > logoMaxSide || height > logoMaxSide {
		return nil, errors.New("icon of an impossible size")
	}
	if bits != 32 && bits != 24 && bits != 8 && bits != 4 && bits != 1 {
		return nil, fmt.Errorf("icon of %d bits", bits)
	}
	palette := 0
	if bits <= 8 {
		palette = 1 << bits
	}
	colors := headerSize + palette*4
	stride := ((width*bits + 31) / 32) * 4
	rows := stride * height
	if len(b) < colors+rows {
		return nil, errors.New("icon rows outside the bitmap")
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	for y := 0; y < height; y++ {
		// The rows are stored bottom-up.
		row := b[colors+(height-1-y)*stride:]
		for x := 0; x < width; x++ {
			var r, g, bl, a uint8
			switch bits {
			case 32:
				p := row[x*4:]
				bl, g, r, a = p[0], p[1], p[2], p[3]
			case 24:
				p := row[x*3:]
				bl, g, r, a = p[0], p[1], p[2], 0xff
			case 8:
				c := b[headerSize+int(row[x])*4:]
				bl, g, r, a = c[0], c[1], c[2], 0xff
			case 4:
				v := row[x/2]
				index := int(v>>4) & 0xf
				if x%2 == 1 {
					index = int(v & 0xf)
				}
				c := b[headerSize+index*4:]
				bl, g, r, a = c[0], c[1], c[2], 0xff
			case 1:
				v := row[x/8]
				index := int(v>>(7-uint(x%8))) & 1
				c := b[headerSize+index*4:]
				bl, g, r, a = c[0], c[1], c[2], 0xff
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, bl, a
			if a != 0 {
				hasAlpha = true
			}
		}
	}
	// An icon whose alpha channel is all zero has none: every pixel is
	// opaque until the mask says otherwise.
	if bits == 32 && !hasAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	}
	// A mask follows the rows: one bit per pixel, set where the pixel is
	// see-through. It is what an icon without an alpha channel says, and
	// one whose alpha channel says nothing at all.
	if bits != 32 || !hasAlpha {
		maskStride := ((width + 31) / 32) * 4
		if start := colors + rows; len(b) >= start+maskStride*height {
			for y := 0; y < height; y++ {
				row := b[start+(height-1-y)*maskStride:]
				for x := 0; x < width; x++ {
					if row[x/8]>>(7-uint(x%8))&1 == 0 {
						continue
					}
					i := img.PixOffset(x, y)
					img.Pix[i+3] = 0
				}
			}
		}
	}
	return img, nil
}
