package helps

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// StripClaudeRequestMarkers removes billing CCH before user payload rules run.
// Other billing fields, thinking controls, and ordinary prompt text survive.
func StripClaudeRequestMarkers(body []byte) []byte {
	doc := ojson.NewDoc(body)
	system := doc.Get("system")
	changed := false
	if system.Type == ojson.String {
		cleaned := stripClaudeBillingCCH(system.String())
		if cleaned != system.String() {
			_ = doc.Set("system", cleaned)
			changed = true
		}
	} else if system.IsArray() {
		system.ForEach(func(index, block ojson.Result) bool {
			if block.Get("type").String() == "text" {
				text := block.Get("text").String()
				cleaned := stripClaudeBillingCCH(text)
				if cleaned != text {
					_ = doc.Set("system."+strconv.FormatInt(index.Int(), 10)+".text", cleaned)
					changed = true
				}
			}
			return true
		})
	}
	if !changed {
		return body
	}
	return doc.Bytes()
}

func stripClaudeBillingCCH(text string) string {
	if !util.IsClaudeCodeAttributionSystemText(text) {
		return text
	}
	const prefix = "x-anthropic-billing-header:"
	start := strings.Index(text, prefix) + len(prefix)
	end := len(text)
	if newline := strings.IndexAny(text[start:], "\r\n"); newline >= 0 {
		end = start + newline
	}
	var kept strings.Builder
	kept.WriteString(text[:start])
	removed := false
	for _, field := range strings.SplitAfter(text[start:end], ";") {
		if strings.HasPrefix(strings.TrimSpace(field), "cch=") {
			removed = true
			continue
		}
		kept.WriteString(field)
	}
	if !removed {
		return text
	}
	kept.WriteString(text[end:])
	return kept.String()
}
