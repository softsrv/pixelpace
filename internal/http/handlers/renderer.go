package handlers

import (
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

// TemplateRenderer holds a base template (layout + partials) and renders pages
// by cloning it per-request, then parsing the page-specific file into the clone.
// This avoids the Go template gotcha where all {{define "content"}} blocks in a
// shared template set overwrite each other globally.
type TemplateRenderer struct {
	base    *template.Template
	fsys    fs.FS
	pageDir string // root path within fsys, e.g. "templates"
}

// NewTemplateRenderer constructs a renderer from a base template.
// base should already contain base.html and all partials.
// fsys is the filesystem to load page templates from; pageDir is the root path within it.
func NewTemplateRenderer(base *template.Template, fsys fs.FS, pageDir string) *TemplateRenderer {
	return &TemplateRenderer{base: base, fsys: fsys, pageDir: pageDir}
}

// Page clones the base template, parses the given page file into the clone, and
// executes "base.html" so that the page's {{define "content"}} overrides the block.
// pagePath is relative to pageDir (e.g. "auth/login.html").
func (r *TemplateRenderer) Page(w http.ResponseWriter, status int, pagePath string, data any) {
	t, err := r.base.Clone()
	if err != nil {
		slog.Error("clone base template", "page", pagePath, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if _, err = t.ParseFS(r.fsys, fmt.Sprintf("%s/%s", r.pageDir, pagePath)); err != nil {
		slog.Error("parse page template", "page", pagePath, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		// Header is already written; we cannot change the status code.
		slog.Error("execute page template", "page", pagePath, "error", err)
	}
}

// Partial executes a named partial template (e.g. "partials/error.html") from a
// clone of the shared base set. Cloning (rather than executing r.base directly)
// keeps r.base in an unexecuted state so Page() can continue to clone it;
// html/template forbids cloning a template after it has been executed.
func (r *TemplateRenderer) Partial(w http.ResponseWriter, status int, name string, data any) {
	t, err := r.base.Clone()
	if err != nil {
		slog.Error("clone base template for partial", "name", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		// Header is already written; we cannot change the status code.
		slog.Error("execute partial template", "name", name, "error", err)
	}
}
