package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pngIcon is an icon as a site serves it: a picture the window draws.
func pngIcon(t *testing.T, side int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.SetNRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// icoIcon is an icon in the form older sites serve: a directory whose one
// entry is a bitmap with a header, rows bottom-up and an optional mask.
func icoIcon(t *testing.T, side int, fill color.NRGBA, cornerVisible bool) []byte {
	t.Helper()
	stride := side * 4
	rows := stride * side
	maskStride := ((side + 31) / 32) * 4
	payload := make([]byte, 40+rows+maskStride*side)
	binary.LittleEndian.PutUint32(payload[0:4], 40)
	binary.LittleEndian.PutUint32(payload[4:8], uint32(side))
	binary.LittleEndian.PutUint32(payload[8:12], uint32(side*2)) // the mask is counted too
	binary.LittleEndian.PutUint16(payload[12:14], 1)
	binary.LittleEndian.PutUint16(payload[14:16], 32)
	binary.LittleEndian.PutUint32(payload[20:24], uint32(rows))
	for y := 0; y < side; y++ {
		row := payload[40+(side-1-y)*stride:]
		for x := 0; x < side; x++ {
			// Every pixel carries the same colour and no alpha, as an
			// icon that only has a mask does.
			p := row[x*4:]
			p[0], p[1], p[2], p[3] = fill.B, fill.G, fill.R, 0
		}
	}
	if !cornerVisible {
		mask := payload[40+rows:]
		mask[0] |= 0x80 // 1 is see-through
	}
	out := make([]byte, 6+16+len(payload))
	binary.LittleEndian.PutUint16(out[2:4], 1)
	binary.LittleEndian.PutUint16(out[4:6], 1)
	out[6], out[7] = byte(side), byte(side)
	binary.LittleEndian.PutUint16(out[6+4:6+6], 1)
	binary.LittleEndian.PutUint16(out[6+6:6+8], 32)
	binary.LittleEndian.PutUint32(out[6+8:6+12], uint32(len(payload)))
	binary.LittleEndian.PutUint32(out[6+12:6+16], uint32(6+16))
	copy(out[6+16:], payload)
	return out
}

// logoServe is the sources' answers: a path of a domain and the status and
// body it gives. A host that is not named does not answer at all, as one
// that cannot be reached.
type logoServe struct {
	mu       sync.Mutex
	answers  map[string]logoAnswer
	requests []string
}

type logoAnswer struct {
	status  int
	body    []byte
	offline bool
}

func (l *logoServe) roundTrip(req *http.Request) (*http.Response, error) {
	l.mu.Lock()
	l.requests = append(l.requests, req.URL.String())
	answer, ok := l.answers[req.URL.Host+req.URL.Path]
	l.mu.Unlock()
	if !ok || answer.offline {
		return nil, errors.New("no such host")
	}
	return &http.Response{
		StatusCode: answer.status, Status: http.StatusText(answer.status),
		Body: io.NopCloser(bytes.NewReader(answer.body)), Header: http.Header{},
	}, nil
}

func (l *logoServe) asked() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func (l *logoServe) set(host, path string, status int, body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers[host+path] = logoAnswer{status: status, body: body}
}

func (l *logoServe) unreachable(host, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers[host+path] = logoAnswer{offline: true}
}

// iconEnv is a service with a cache directory and the sources' answers.
func iconEnv(t *testing.T) (*Service, *memStore, string, *logoServe) {
	t.Helper()
	store := newMemStore()
	dir := t.TempDir()
	svc := NewService(store, newMemSecrets(), dir)
	fake := &logoServe{answers: map[string]logoAnswer{}}
	svc.logoClient = func(ctx context.Context) (*http.Client, error) {
		return &http.Client{Transport: roundTripperFunc(fake.roundTrip)}, nil
	}
	return svc, store, dir, fake
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func iconOf(t *testing.T, side int) color.NRGBA {
	t.Helper()
	return color.NRGBA{R: 0x11, G: 0x6f, B: 0xef, A: 0xff}
}

// TestLogoDomainSkipsWhatSaysNothingOfTheSender proves the domain an icon
// is looked up by, and the addresses that have none: a mail service's
// icon is the provider's, and one that names no domain is no domain.
func TestLogoDomainSkipsWhatSaysNothingOfTheSender(t *testing.T) {
	cases := []struct {
		email  string
		domain string
		ok     bool
	}{
		{"notifications@github.com", "github.com", true},
		{"Noreply@Vercel.COM", "vercel.com", true},
		{"someone@example.com.", "example.com", true},
		{"someone@iCloud.com", "", false},
		{"someone@gmail.com", "", false},
		{"someone@163.com", "", false},
		{"someone@qq.com", "", false},
		{"someone@127.0.0.1", "", false},
		{"someone@localhost", "", false},
		{"someone@", "", false},
		{"no-at-sign", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		domain, ok := LogoDomain(Address{Email: c.email})
		if domain != c.domain || ok != c.ok {
			t.Errorf("LogoDomain(%q) = %q, %v, want %q, %v", c.email, domain, ok, c.domain, c.ok)
		}
	}
}

// TestSenderIconReadsTheDomainsOwnMark proves a site's own icon is used
// before any icon service is asked, and that it is kept: the same domain
// is asked once.
func TestSenderIconReadsTheDomainsOwnMark(t *testing.T) {
	svc, _, dir, fake := iconEnv(t)
	ctx := context.Background()
	data := pngIcon(t, 32, iconOf(t, 32))
	fake.set("github.com", "/apple-touch-icon.png", http.StatusOK, data)

	got, err := svc.SenderIcon(ctx, Address{Email: "notifications@github.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("icon = %d bytes, want the site's own", len(got))
	}
	// The icon service was not asked: the site answered.
	for _, u := range fake.asked() {
		if bytes.Contains([]byte(u), []byte("duckduckgo")) || bytes.Contains([]byte(u), []byte("google")) {
			t.Errorf("asked %q though the site had an icon", u)
		}
	}
	// The file is in the cache directory, named by its hash.
	entries, err := os.ReadDir(filepath.Join(dir, senderIconCacheDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("icon files = %v, %v", entries, err)
	}
	if stored, err := os.ReadFile(filepath.Join(dir, senderIconCacheDir, entries[0].Name())); err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("kept icon = %d bytes, %v", len(stored), err)
	}

	asked := len(fake.asked())
	again, err := svc.SenderIcon(ctx, Address{Email: "notifications@github.com"})
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("second icon = %d bytes, %v", len(again), err)
	}
	if n := len(fake.asked()); n != asked {
		t.Errorf("%d more requests for a domain already kept", n-asked)
	}
}

// TestSenderIconFallsBackAndDecodesICO proves an icon served only as an ICO
// is decoded, and that a site with neither of its own marks falls to the
// icon service, which learns the domain alone.
func TestSenderIconFallsBackAndDecodesICO(t *testing.T) {
	svc, _, _, fake := iconEnv(t)
	ctx := context.Background()

	ico := icoIcon(t, 16, iconOf(t, 16), true)
	fake.set("vercel.com", "/apple-touch-icon.png", http.StatusNotFound, nil)
	fake.set("vercel.com", "/favicon.ico", http.StatusOK, ico)

	got, err := svc.SenderIcon(ctx, Address{Email: "noreply@vercel.com"})
	if err != nil || len(got) == 0 {
		t.Fatalf("icon = %d bytes, %v", len(got), err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("the icon is no PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 16 || b.Dy() != 16 {
		t.Errorf("icon = %v, want 16×16", b)
	}
	if r, g, b, a := img.At(8, 8).RGBA(); a == 0 || r == 0 || g == 0 || b == 0 {
		t.Errorf("icon centre = %d %d %d %d, want the site's colour", r>>8, g>>8, b>>8, a>>8)
	}

	// A domain with no icon of its own is asked at the icon service, and
	// what it answers is kept as it is.
	fallback := pngIcon(t, 64, iconOf(t, 64))
	fake.set("github.com", "/apple-touch-icon.png", http.StatusNotFound, nil)
	fake.set("github.com", "/favicon.ico", http.StatusOK, []byte("<html>no</html>"))
	fake.set("icons.duckduckgo.com", "/ip3/github.com.ico", http.StatusOK, fallback)
	got, err = svc.SenderIcon(ctx, Address{Email: "notifications@github.com"})
	if err != nil || !bytes.Equal(got, fallback) {
		t.Fatalf("fallback icon = %d bytes, %v", len(got), err)
	}
}

// TestSenderIconDecodesAnICOsMask proves a pixel the icon's mask marks
// see-through stays so, as the mark of a site with a plain background
// needs.
func TestSenderIconDecodesAnICOsMask(t *testing.T) {
	data := icoIcon(t, 16, iconOf(t, 16), false)
	pnged, err := iconPNG(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pnged))
	if err != nil {
		t.Fatal(err)
	}
	// The mask's first byte is the bottom row's first pixel, as the rows
	// run bottom-up.
	if _, _, _, a := img.At(0, 15).RGBA(); a != 0 {
		t.Errorf("masked corner alpha = %d, want 0", a>>8)
	}
	if _, _, _, a := img.At(8, 8).RGBA(); a == 0 {
		t.Error("the square inside the mask is see-through")
	}
}

// TestSenderIconRecordsWhatItFound proves a lookup that found nothing is
// kept, so a domain is asked once in a month, and that one that could not
// reach a source is kept nowhere, so coming back online finds its icon.
func TestSenderIconRecordsWhatItFound(t *testing.T) {
	svc, store, _, fake := iconEnv(t)
	ctx := context.Background()
	for _, path := range []string{"/apple-touch-icon.png", "/favicon.ico"} {
		fake.set("nologo.example", path, http.StatusNotFound, nil)
	}
	fake.set("icons.duckduckgo.com", "/ip3/nologo.example.ico", http.StatusNotFound, nil)
	fake.set("www.google.com", "/s2/favicons", http.StatusNotFound, nil)

	if got, err := svc.SenderIcon(ctx, Address{Email: "a@nologo.example"}); err != nil || got != nil {
		t.Fatalf("icon = %d bytes, %v, want none", len(got), err)
	}
	if icon, ok, _ := store.SenderIcon(ctx, "domain:nologo.example"); !ok || icon.Found {
		t.Errorf("kept %+v, want a lookup that found none", icon)
	}
	asked := len(fake.asked())
	if _, err := svc.SenderIcon(ctx, Address{Email: "a@nologo.example"}); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.asked()); n != asked {
		t.Errorf("%d more requests within the month", n-asked)
	}
	// Once the month is over, it is asked again: a site that gains an
	// icon meanwhile is found late, not never.
	svc.SetClock(func() time.Time { return time.Now().Add(logoRetry + time.Hour) })
	if _, err := svc.SenderIcon(ctx, Address{Email: "a@nologo.example"}); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.asked()); n == asked {
		t.Error("the domain was not asked again after the wait")
	}

	// Nothing answers: no row, so the next attempt is not a month away.
	for _, host := range []string{"offline.example", "icons.duckduckgo.com", "www.google.com"} {
		for _, path := range []string{"/apple-touch-icon.png", "/favicon.ico", "/ip3/offline.example.ico", "/s2/favicons"} {
			fake.unreachable(host, path)
		}
	}
	if _, err := svc.SenderIcon(ctx, Address{Email: "a@offline.example"}); err == nil {
		t.Fatal("an unreachable lookup said it was fine")
	}
	if _, ok, _ := store.SenderIcon(ctx, "domain:offline.example"); ok {
		t.Error("an unreachable lookup was kept as one that found nothing")
	}
}

// TestSenderIconAsksAgainForAFileTheSweepTook proves a row whose file is
// gone finds its icon again, rather than showing nothing for a month.
func TestSenderIconAsksAgainForAFileTheSweepTook(t *testing.T) {
	svc, store, dir, fake := iconEnv(t)
	ctx := context.Background()
	data := pngIcon(t, 32, iconOf(t, 32))
	fake.set("github.com", "/apple-touch-icon.png", http.StatusOK, data)
	if _, err := svc.SenderIcon(ctx, Address{Email: "a@github.com"}); err != nil {
		t.Fatal(err)
	}
	icon, _, _ := store.SenderIcon(ctx, "domain:github.com")
	if err := os.Remove(filepath.Join(dir, senderIconCacheDir, icon.ContentHash)); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.SenderIcon(ctx, Address{Email: "a@github.com"}); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("icon = %d bytes, %v, want the site's answer again", len(got), err)
	}
}

// TestSweepCacheRemovesIconsNoRowNames proves the sweep takes an icon file
// no row names, and keeps the one that a row does.
func TestSweepCacheRemovesIconsNoRowNames(t *testing.T) {
	svc, store, dir, fake := iconEnv(t)
	ctx := context.Background()
	data := pngIcon(t, 32, iconOf(t, 32))
	fake.set("github.com", "/apple-touch-icon.png", http.StatusOK, data)
	if _, err := svc.SenderIcon(ctx, Address{Email: "a@github.com"}); err != nil {
		t.Fatal(err)
	}
	dirIcons := filepath.Join(dir, senderIconCacheDir)
	if err := os.WriteFile(filepath.Join(dirIcons, "orphan"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The sweep leaves a file younger than its grace, which is a copy in
	// progress; an older service clock makes both of them old.
	svc.started = time.Now().Add(-2 * cacheGrace)
	svc.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{"orphan"} {
		if err := os.Chtimes(filepath.Join(dirIcons, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	kept, _, _ := store.SenderIcon(ctx, "domain:github.com")
	if err := os.Chtimes(filepath.Join(dirIcons, kept.ContentHash), old, old); err != nil {
		t.Fatal(err)
	}
	if err := svc.SweepCache(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirIcons, "orphan")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan icon file left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirIcons, kept.ContentHash)); err != nil {
		t.Errorf("kept icon file gone: %v", err)
	}
}

// TestIconKeysNameThePersonThenTheHost proves what is looked for: the
// person an address names at the host its mail comes from, then the mark
// of that host, else the sender's own domain.
func TestIconKeysNameThePersonThenTheHost(t *testing.T) {
	cases := []struct {
		email string
		keys  []string
	}{
		{"octocat@users.noreply.github.com", []string{"profile:github.com/octocat", "domain:github.com"}},
		{"583231+octocat@users.noreply.github.com", []string{"profile:github.com/octocat", "domain:github.com"}},
		// A bare number is an id elsewhere: it names no account to ask for.
		{"583231@users.noreply.github.com", []string{"domain:github.com"}},
		{"gitlab-org@users.noreply.gitlab.com", []string{"profile:gitlab.com/gitlab-org", "domain:gitlab.com"}},
		{"bad/user@users.noreply.github.com", []string{"domain:github.com"}},
		{"notifications@github.com", []string{"domain:github.com"}},
		{"someone@gmail.com", nil},
		{"someone@localhost", nil},
	}
	for _, c := range cases {
		if got := iconKeys(Address{Email: c.email}); strings.Join(got, " ") != strings.Join(c.keys, " ") {
			t.Errorf("iconKeys(%q) = %v, want %v", c.email, got, c.keys)
		}
	}
}

// TestSenderIconShowsThePersonsPictureFirst proves a person's own picture
// is what shows where the address names one, and that the host's mark is
// what shows when the account has none: a bot writes, and no account.
func TestSenderIconShowsThePersonsPictureFirst(t *testing.T) {
	svc, store, _, fake := iconEnv(t)
	ctx := context.Background()
	photo := pngIcon(t, 96, color.NRGBA{R: 0x33, G: 0x33, B: 0x33, A: 0xff})
	mark := pngIcon(t, 32, iconOf(t, 32))
	fake.set("github.com", "/octocat.png", http.StatusOK, photo)
	fake.set("github.com", "/apple-touch-icon.png", http.StatusOK, mark)

	got, err := svc.SenderIcon(ctx, Address{Email: "octocat@users.noreply.github.com"})
	if err != nil || !bytes.Equal(got, photo) {
		t.Fatalf("icon = %d bytes, %v, want the account's picture", len(got), err)
	}
	if icon, ok, _ := store.SenderIcon(ctx, "profile:github.com/octocat"); !ok || !icon.Found {
		t.Errorf("the picture was not kept: %+v, %v", icon, ok)
	}
	// The mark was not asked for: the picture answered.
	for _, u := range fake.asked() {
		if strings.Contains(u, "apple-touch-icon") || strings.Contains(u, "favicon") {
			t.Errorf("asked %q though the account had a picture", u)
		}
	}

	// An account with no picture falls back to the host's mark.
	fake.set("github.com", "/bot-bot.png", http.StatusNotFound, nil)
	got, err = svc.SenderIcon(ctx, Address{Email: "bot-bot@users.noreply.github.com"})
	if err != nil || !bytes.Equal(got, mark) {
		t.Fatalf("icon = %d bytes, %v, want the host's mark", len(got), err)
	}
}

// TestSenderIconReadsGitLabsOwnPictures proves the API names the picture,
// and that only GitLab's own uploads are taken: a Gravatar is no picture
// this device asked for.
func TestSenderIconReadsGitLabsOwnPictures(t *testing.T) {
	svc, _, _, fake := iconEnv(t)
	ctx := context.Background()
	photo := pngIcon(t, 96, color.NRGBA{R: 0x22, G: 0x44, B: 0x66, A: 0xff})
	fake.set("gitlab.com", "/api/v4/users", http.StatusOK,
		[]byte(`[{"username":"gitlab-org","avatar_url":"https://gitlab.com/uploads/-/system/user/avatar/1/avatar.png"}]`))
	fake.set("gitlab.com", "/uploads/-/system/user/avatar/1/avatar.png", http.StatusOK, photo)
	got, err := svc.SenderIcon(ctx, Address{Email: "gitlab-org@users.noreply.gitlab.com"})
	if err != nil || !bytes.Equal(got, photo) {
		t.Fatalf("icon = %d bytes, %v, want GitLab's own picture", len(got), err)
	}

	// An account whose picture is a Gravatar keeps the host's mark.
	mark := pngIcon(t, 32, iconOf(t, 32))
	fake.set("gitlab.com", "/api/v4/users", http.StatusOK,
		[]byte(`[{"username":"mateo","avatar_url":"https://secure.gravatar.com/avatar/x"}]`))
	fake.set("gitlab.com", "/apple-touch-icon.png", http.StatusOK, mark)
	fake.set("gitlab.com", "/favicon.ico", http.StatusNotFound, nil)
	got, err = svc.SenderIcon(ctx, Address{Email: "mateo@users.noreply.gitlab.com"})
	if err != nil || !bytes.Equal(got, mark) {
		t.Fatalf("icon = %d bytes, %v, want the host's mark", len(got), err)
	}

}

// TestSenderIconKeepsNoVerdictOfABusySource proves a rate limit is no
// answer: nothing is recorded for the account, so the next attempt is not
// a month away.
func TestSenderIconKeepsNoVerdictOfABusySource(t *testing.T) {
	svc, store, _, fake := iconEnv(t)
	ctx := context.Background()
	fake.set("gitlab.com", "/api/v4/users", http.StatusTooManyRequests, nil)
	for _, path := range []string{"/apple-touch-icon.png", "/favicon.ico"} {
		fake.set("gitlab.com", path, http.StatusNotFound, nil)
	}
	fake.set("icons.duckduckgo.com", "/ip3/gitlab.com.ico", http.StatusNotFound, nil)
	fake.set("www.google.com", "/s2/favicons", http.StatusNotFound, nil)
	if _, err := svc.SenderIcon(ctx, Address{Email: "busy@users.noreply.gitlab.com"}); err == nil {
		t.Error("a rate limit said the account had no picture")
	}
	if _, ok, _ := store.SenderIcon(ctx, "profile:gitlab.com/busy"); ok {
		t.Error("a rate limit was kept as an account with no picture")
	}
}
