// Package web embeds the panel's templates and static assets.
package web

import "embed"

// Templates holds templates/{layouts,components,pages}/*.html.
//
//go:embed templates
var Templates embed.FS

// Static holds static/* (htmx, css), served under /static/.
//
//go:embed static/*
var Static embed.FS
