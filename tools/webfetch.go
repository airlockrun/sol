package tools

import (
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/webfetch"
)

// Webfetch creates the webfetch tool using a direct HTTP client.
func Webfetch() tool.Tool {
	return webfetch.NewTool(webfetch.NewClient())
}
