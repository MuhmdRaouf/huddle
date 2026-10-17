package web

import (
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

// index.html puts the theme on <html> before first paint through theme-boot.js, loaded ahead of
// the app and never inline: the CSP the hub serves allows no inline script.
func TestIndexHTMLBootsTheThemeFromAFile(t *testing.T) {
	html, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	page := string(html)
	boot := strings.Index(page, `src="/static/theme-boot.js"`)
	app := strings.Index(page, `src="/static/app.js"`)
	if boot < 0 || app < 0 || boot > app {
		t.Fatal("index.html must load /static/theme-boot.js before /static/app.js")
	}
	if strings.Contains(page, "<script>") {
		t.Fatal("index.html holds an inline script; the CSP allows none")
	}
	if _, err := fs.ReadFile(FS(), "theme-boot.js"); err != nil {
		t.Fatalf("read theme-boot.js: %v", err)
	}
}

// The PWA ships with the shell: the manifest, the service worker with its cache revision stamped
// by the build (never the placeholder), the three icons the manifest names, and the page linking
// the manifest.
func TestEmbedsThePWA(t *testing.T) {
	t.Run("index.html links the manifest and names the theme colour", func(t *testing.T) {
		page, err := fs.ReadFile(FS(), "index.html")
		if err != nil {
			t.Fatalf("read index.html: %v", err)
		}
		html := string(page)
		if !strings.Contains(html, `rel="manifest"`) || !strings.Contains(html, "/manifest.webmanifest") {
			t.Fatal("index.html does not link /manifest.webmanifest")
		}
		if !strings.Contains(html, `name="theme-color"`) {
			t.Fatal("index.html names no theme colour")
		}
	})

	t.Run("the manifest describes Huddle as an installable app", func(t *testing.T) {
		raw, err := fs.ReadFile(FS(), "manifest.webmanifest")
		if err != nil {
			t.Fatalf("read manifest.webmanifest: %v", err)
		}
		var m struct {
			Name            string `json:"name"`
			ShortName       string `json:"short_name"`
			Display         string `json:"display"`
			StartURL        string `json:"start_url"`
			BackgroundColor string `json:"background_color"`
			ThemeColor      string `json:"theme_color"`
			Icons           []struct {
				Src     string `json:"src"`
				Sizes   string `json:"sizes"`
				Type    string `json:"type"`
				Purpose string `json:"purpose"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parse manifest.webmanifest: %v", err)
		}
		if m.Name != "Huddle" || m.ShortName != "Huddle" || m.Display != "standalone" {
			t.Fatalf("manifest names %q / %q shown %q", m.Name, m.ShortName, m.Display)
		}
		if m.StartURL != "/#/now" {
			t.Fatalf("start_url = %q, want /#/now", m.StartURL)
		}
		if m.BackgroundColor != "#11111b" || m.ThemeColor != "#11111b" {
			t.Fatalf("colours %q / %q, want #11111b", m.BackgroundColor, m.ThemeColor)
		}
		sizes := map[string]bool{}
		for _, icon := range m.Icons {
			if icon.Type != "image/png" {
				t.Fatalf("icon %s carries type %q", icon.Src, icon.Type)
			}
			sizes[icon.Sizes+" "+icon.Purpose] = true
		}
		for _, want := range []string{"192x192 ", "512x512 ", "512x512 maskable"} {
			if !sizes[want] {
				t.Fatalf("the icons miss a %q entry: %v", want, m.Icons)
			}
		}
	})

	t.Run("the service worker routes fetches from a versioned cache", func(t *testing.T) {
		raw, err := fs.ReadFile(FS(), "sw.js")
		if err != nil {
			t.Fatalf("read sw.js: %v", err)
		}
		sw := string(raw)
		if !strings.Contains(sw, `addEventListener("fetch"`) {
			t.Fatal("the worker catches no fetch")
		}
		if !strings.Contains(sw, "huddle-shell-") {
			t.Fatal("the worker names no versioned cache")
		}
		if strings.Contains(sw, "@@REV@@") {
			t.Fatal("the worker ships with its revision placeholder unstamped")
		}
	})

	t.Run("the three icons are PNGs of the sizes the manifest names", func(t *testing.T) {
		for name, size := range map[string]int{"icon-192.png": 192, "icon-512.png": 512, "icon-maskable-512.png": 512} {
			data, err := fs.ReadFile(FS(), "icons/"+name)
			if err != nil {
				t.Fatalf("read icons/%s: %v", name, err)
			}
			if len(data) < 100 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
				t.Fatalf("icons/%s is not a PNG", name)
			}
			if w := int(binary.BigEndian.Uint32(data[16:20])); w != size {
				t.Fatalf("icons/%s is %d wide, want %d", name, w, size)
			}
			if h := int(binary.BigEndian.Uint32(data[20:24])); h != size {
				t.Fatalf("icons/%s is %d tall, want %d", name, h, size)
			}
		}
	})
}

// FS() serves the built dashboard with dist/ as its root, so index.html and every
// asset sit at the names the server hands to browsers.
func TestEmbedsTheBuiltDashboard(t *testing.T) {
	t.Run("index.html is reachable and non-empty", func(t *testing.T) {
		data, err := fs.ReadFile(FS(), "index.html")
		if err != nil {
			t.Fatalf("read index.html: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("index.html is empty")
		}
	})

	t.Run("every file under dist is reachable and non-empty", func(t *testing.T) {
		seen := 0
		err := fs.WalkDir(FS(), ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			seen++
			data, err := fs.ReadFile(FS(), name)
			if err != nil {
				t.Errorf("read %s: %v", name, err)
				return nil
			}
			if len(data) == 0 {
				t.Errorf("%s is empty", name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen == 0 {
			t.Fatal("dist holds no files")
		}
	})
}
