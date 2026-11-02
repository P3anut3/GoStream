package httpx

import (
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// Vue uses hash routes, so unknown API paths keep their real 404 responses.
// Only compiled files are served; source code and node_modules stay private.
func RegisterWeb(router *gin.Engine, root string) {
	index := func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.File(filepath.Join(root, "index.html"))
	}
	router.GET("/", index)
	router.HEAD("/", index)
	router.StaticFS("/assets", gin.Dir(filepath.Join(root, "assets"), false))
	router.StaticFile("/cover.svg", filepath.Join(root, "cover.svg"))
}
