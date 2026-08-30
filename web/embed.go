package web

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"ehang.io/nps/lib/common"
	"github.com/astaxie/beego/logs"
)

//go:embed static
var StaticFS embed.FS

//go:embed views
var ViewsFS embed.FS

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
