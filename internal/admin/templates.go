package admin

import (
	"embed"
	"html/template"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

var templateFuncs = template.FuncMap{
	"join":     strings.Join,
	"truncate": truncate,
}

var loginTemplate = template.Must(template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/login.html"))

var pageTemplates = func() map[string]*template.Template {
	base := template.Must(template.New("").Funcs(templateFuncs).ParseFS(templateFS,
		"templates/layout.html",
		"templates/_flash.html",
	))
	pages := []string{
		"dashboard.html",
		"raw.html",
		"routes_list.html",
		"route_form.html",
		"section_json.html",
	}
	out := map[string]*template.Template{}
	for _, page := range pages {
		out[page] = template.Must(template.Must(base.Clone()).ParseFS(templateFS, "templates/"+page))
	}
	return out
}()

func truncate(value string, n int) string {
	runes := []rune(value)
	if n <= 0 || len(runes) <= n {
		return value
	}
	return string(runes[:n]) + "..."
}
