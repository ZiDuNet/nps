package web

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

//go:embed static
var StaticFS embed.FS

//go:embed views
var ViewsFS embed.FS

// obsoleteStaticAssets lists files removed from the embedded bundle. Upgraded
// installations keep a copy in the runtime web directory, so remove the known
// ZUI artifacts during startup to make the switch to Bootstrap complete.
var obsoleteStaticAssets = []string{
	"static/css/zui-3.0.0.css",
	"static/css/zui-console.css",
	"static/js/zui-3.0.0.js",
	"static/webfonts/zenicon.svg",
	"static/webfonts/zenicon.ttf",
	"static/webfonts/zenicon.woff",
	"static/licenses/zui-3.0.0-MIT.txt",
}

func ExtractWebFiles(runPath string) {
	webDir := filepath.Join(runPath, "web")
	staticDir := filepath.Join(webDir, "static")
	viewsDir := filepath.Join(webDir, "views")

	// Always sync embedded static assets so upgraded JS/CSS is not masked by
	// files left from an earlier server version.
	extractFS(StaticFS, "static", webDir)
	logs.Info("Synced embedded web/static to", staticDir)
	extractFS(ViewsFS, "views", webDir)
	logs.Info("Synced embedded web/views to", viewsDir)
	removeObsoleteStaticAssets(webDir)
}

func removeObsoleteStaticAssets(webDir string) {
	webRoot := filepath.Clean(webDir) + string(filepath.Separator)
	for _, rel := range obsoleteStaticAssets {
		target := filepath.Join(webDir, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, webRoot) {
			logs.Warning("Refused to remove static asset outside web directory:", target)
			continue
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			logs.Warning("Could not remove obsolete static asset:", target, err)
		}
	}
}

// ReadStaticFile reads a file from the embedded static filesystem.
func ReadStaticFile(name string) ([]byte, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Clean("/" + strings.TrimPrefix(name, "/"))
	return StaticFS.ReadFile(path.Join("static", strings.TrimPrefix(name, "/")))
}

type slashFileSystem struct{ fs http.FileSystem }

func (s slashFileSystem) Open(name string) (http.File, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	return s.fs.Open(strings.TrimPrefix(name, "/"))
}
func ViewsHTTPFS() http.FileSystem { return slashFileSystem{fs: http.FS(ViewsFS)} }
func StaticHTTPFS() http.FileSystem {
	sub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		panic(err)
	}
	return slashFileSystem{fs: http.FS(sub)}
}
func InitBeegoAssets() {
	beego.SetTemplateFSFunc(func() http.FileSystem { return ViewsHTTPFS() })
	beego.SetViewsPath("views")
	beego.DelStaticPath("/static")
	prefix := strings.TrimSuffix(beego.AppConfig.String("web_base_url"), "/") + "/static"
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	beego.Handler(prefix, http.StripPrefix(prefix, http.FileServer(StaticHTTPFS())), true)
}

func extractFS(efs embed.FS, root string, destDir string) {
	fs.WalkDir(efs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		targetPath := filepath.Join(destDir, path)
		if d.IsDir() {
			os.MkdirAll(targetPath, 0755)
			return nil
		}
		data, err := efs.ReadFile(path)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(targetPath), 0755)
		os.WriteFile(targetPath, data, 0644)
		return nil
	})
}
