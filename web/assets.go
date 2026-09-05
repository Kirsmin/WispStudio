package web

import "embed"

// Static contains the complete dependency-free frontend.
//
//go:embed static/*
var Static embed.FS
