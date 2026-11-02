package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
)

//go:embed templates/*.html static
var assets embed.FS

type SystemView struct {
	Name          string
	Version       string
	ServerTime    string
	Heartbeat     string
	WorkerHealthy bool
	Error         bool
}

type Renderer struct{ templates *template.Template }

func NewRenderer() (*Renderer, error) {
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Renderer{templates: templates}, nil
}

func (r *Renderer) Render(name string, value SystemView) ([]byte, error) {
	var buffer bytes.Buffer
	if err := r.templates.ExecuteTemplate(&buffer, name, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func Static() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
