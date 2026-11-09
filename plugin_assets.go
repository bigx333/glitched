package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins/render"
)

// The bounds of a plugin's pictures.
const (
	maxPluginSVG   = 32 << 10
	maxPluginImage = 4 << 20
)

// asset is a plugin's picture, as it loads: nil until it has, and for
// good when it can't.
type asset struct {
	src    ui.ImageSource
	svg    *ui.SVG
	loaded bool
}

// pluginStyle is the look of the plugin id's widgets: the theme's colors,
// the app's icons, and pictures from the plugin's folder.
func (a *app) pluginStyle(id string) render.Style {
	return render.Style{
		Text: palette.text, Muted: palette.muted, Faint: palette.faint, Accent: palette.accent,
		Red: palette.red, Green: palette.green, Amber: palette.amber, Border: palette.border, Surface: palette.surface,
		Card:  func(c *ui.Context, title string, body func()) { card(c, strings.ToUpper(title), body) },
		Icon:  func(name string) *ui.SVG { return a.pluginIcon(id, name) },
		Image: func(src string) ui.ImageSource { return a.pluginImage(id, src) },
	}
}

// appIconNamed is the app's icon of that name, nil for none. The names are
// those of icons/, and a few short ones (pluginIcons).
func appIconNamed(name string) *ui.SVG {
	if s := pluginIcons[name]; s != nil {
		return s
	}
	if strings.ContainsAny(name, "/.") {
		return nil
	}
	if _, err := fs.Stat(iconFiles, "icons/"+name+".svg"); err != nil {
		return nil
	}
	return solar(name)
}

// pluginIcon is the icon a widget names: the app's, or an SVG of the
// plugin's own ("plugin:icon.svg"), drawn in the text's color.
func (a *app) pluginIcon(id, name string) *ui.SVG {
	rel, own := strings.CutPrefix(name, "plugin:")
	if !own {
		return appIconNamed(name)
	}
	as := a.pluginAsset(id, "icon:"+rel, func(b []byte) asset {
		if len(b) > maxPluginSVG {
			return asset{loaded: true}
		}
		s, err := ui.ParseSVG(b)
		if err != nil {
			return asset{loaded: true}
		}
		return asset{svg: s, loaded: true}
	}, rel)
	return as.svg
}

// pluginImage is a picture of the plugin's folder ("plugin:cover.png"):
// PNG, JPEG or SVG. Remote pictures aren't drawn yet.
func (a *app) pluginImage(id, src string) ui.ImageSource {
	rel, own := strings.CutPrefix(src, "plugin:")
	if !own {
		return nil
	}
	as := a.pluginAsset(id, "image:"+rel, func(b []byte) asset {
		if len(b) > maxPluginImage {
			return asset{loaded: true}
		}
		if bytes.Contains(b[:min(len(b), 512)], []byte("<svg")) {
			if s, err := ui.ParseSVG(b); err == nil {
				return asset{src: s, loaded: true}
			}
			return asset{loaded: true}
		}
		bm, err := ui.DecodeBitmap(b)
		if err != nil {
			return asset{loaded: true}
		}
		return asset{src: bm, loaded: true}
	}, rel)
	return as.src
}

// pluginAsset reads a file of the plugin's folder off the UI thread, once,
// and keeps what decode made of it.
func (a *app) pluginAsset(id, key string, decode func([]byte) asset, rel string) asset {
	k := id + "\x00" + key
	if as, ok := a.pluginAssets[k]; ok {
		return as
	}
	a.pluginAssets[k] = asset{}
	folder := a.plugins.Folder(id)
	go func() {
		path := filepath.Join(folder, filepath.FromSlash(rel))
		var as asset
		if folder == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
			as = asset{loaded: true}
		} else if b, err := os.ReadFile(path); err != nil {
			as = asset{loaded: true}
		} else {
			as = decode(b)
		}
		a.post(func() { a.pluginAssets[k] = as })
	}()
	return asset{}
}
