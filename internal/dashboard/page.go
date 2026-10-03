package dashboard

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed dashboard.html
var pageHTML []byte

// ServePage writes the self-contained routing dashboard. The page reads its data from the
// authenticated /v8/management/routing/overview endpoint, so it carries no secrets itself.
func ServePage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
	c.Data(http.StatusOK, "text/html; charset=utf-8", pageHTML)
}
