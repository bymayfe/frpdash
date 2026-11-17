package server

import (
	"errors"
	"fmt"
	"html/template"

	assets "github.com/bymayfe/frpdash/web"
)

// Templates are organised like a component-based frontend:
//
//	web/templates/layouts/     the page shell ("base")
//	web/templates/components/  reusable pieces, called with {{ template "name" ... }}
//	web/templates/pages/       one file per page, defining "title" and "content"
//
// Every page is parsed together with all layouts and components, so any page
// can use any component.

// funcs are available in every template.
var funcs = template.FuncMap{
	"dict": dict,
}

// parsePage parses one page with the shared layouts and components.
func parsePage(name string) (*template.Template, error) {
	t, err := template.New(name).Funcs(funcs).ParseFS(assets.Templates,
		"templates/layouts/*.html",
		"templates/components/*.html",
		"templates/pages/"+name+".html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse page %s: %w", name, err)
	}
	return t, nil
}

// dict builds a map from key/value pairs, so a component can take several
// named parameters, much like props:
//
//	{{ template "page-header" (dict "Title" "Clients" "ActionHref" "/clients/new") }}
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("dict needs key/value pairs")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %v is not a string", pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}
