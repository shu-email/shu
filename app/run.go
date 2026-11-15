package app

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// keychain adapts mygo.Secrets to the engine's SecretStore.
type keychain struct{}

func (keychain) Set(key string, secret []byte) error { return mygo.Secrets.Set(key, secret) }
func (keychain) Get(key string) ([]byte, error) {
	data, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, mail.ErrNotFound
	}
	return data, err
}
func (keychain) Delete(key string) error { return mygo.Secrets.Delete(key) }

// noteClicks holds the notification clicks that come before the window
// can take them: the click that launched the app arrives as it starts.
type noteClicks struct {
	mu      sync.Mutex
	waiting []string
	open    func(id string)
}

// add takes a click, for the window if it is there and for later if not.
func (q *noteClicks) add(id string) {
	q.mu.Lock()
	open := q.open
	if open == nil {
		q.waiting = append(q.waiting, id)
	}
	q.mu.Unlock()
	if open != nil {
		open(id)
	}
}

// deliver hands the clicks so far to open, and every later one as it
// comes.
func (q *noteClicks) deliver(open func(id string)) {
	q.mu.Lock()
	waiting := q.waiting
	q.waiting, q.open = nil, open
	q.mu.Unlock()
	for _, id := range waiting {
		open(id)
	}
}

// seedDev, set by seed_e2e.go in the dev builds it is linked into, has
// the engine use local mail servers when SHU_SEED=1 asks for them.
var seedDev func(ctx context.Context, svc *mail.Service) error

// Run starts the app and shows its window. It returns when the app quits.
func Run() {
	// Registered before Run, as the click on a notification that launched
	// the app is delivered while it starts.
	var clicks noteClicks
	mygo.App.OnNotificationClick(clicks.add)
	mygo.App.WhenReady(func() {
		dataDir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		cacheDir, err := mygo.App.Path(mygo.PathCache)
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sqlite.Load(); err != nil {
			log.Fatalf("sqlite: %v (run go tool mygo generate)", err)
		}
		db, err := store.Open(ctx, filepath.Join(dataDir, "shu.db"))
		if err != nil {
			log.Fatal(err)
		}
		svc := mail.NewService(db, keychain{}, cacheDir)
		// What a draft has attached is the user's data, not a cache that
		// the system may clear: it is kept beside the database.
		svc.SetDataDir(dataDir)
		if seedDev != nil {
			if err := seedDev(ctx, svc); err != nil {
				log.Fatalf("seed: %v", err)
			}
		}
		a := newApp(svc)
		a.ai = ai.NewService(db, aiKeychain{})
		// The AI layer sends through the same proxy and certificate
		// authority as mail.
		if client, err := svc.HTTPClient(ctx); err == nil {
			a.ai.SetHTTPClient(client)
		}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           locales.AppName(),
			Width:           1220,
			Height:          800,
			MinWidth:        1000,
			MinHeight:       640,
			StateKey:        "main",
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			BackgroundColor: "light-dark(#ffffff, #18181b)",
			// The sidebar's material: AppKit's on macOS, Mica on Windows
			// 11. The view draws a background where there is none.
			Vibrancy: mygo.VibrancySidebar,
			Content:  ui.View(a.view),
		})
		a.attach(win)
		chosen := mygo.App.ChosenLocale()
		switch {
		case chosen == "en":
			a.lang = 1
		case chosen == "zh-Hans":
			a.lang = 2
		}
		mygo.App.OnQuit(func() {
			// The watchers end before the database they write to closes.
			svc.StopWatching()
			db.Close()
		})
		clicks.deliver(func(id string) {
			a.post(func() { a.openNotification(id) })
		})
		a.start()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
