package management

import (
	"bytes"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clientconfig"
)

// GetClientConfig returns the settings that point Claude Code or Codex at this proxy as
// plain text, so a new machine can fetch them with its management key. The host defaults to
// the name the caller used to reach this server; ?host= overrides it. Scheme and port always
// come from this server's config, so callers behind a TLS-terminating proxy adjust the URL.
func (h *Handler) GetClientConfig(c *gin.Context) {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		host = requestHostName(c.Request.Host)
	}
	var out bytes.Buffer
	// Render under h.mu so a concurrent config update cannot mix two configs' port and key.
	h.mu.Lock()
	errRender := clientconfig.Render(&out, h.cfg, c.Param("client"), host)
	h.mu.Unlock()
	if errRender != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRender.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", out.Bytes())
}

func requestHostName(hostport string) string {
	if host, _, errSplit := net.SplitHostPort(hostport); errSplit == nil {
		return host
	}
	return hostport
}
