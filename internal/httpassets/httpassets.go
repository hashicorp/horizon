// Package httpassets contain the assets for HTML pages that Horizon
// serves. This is temporary since this is Waypoint-specific and Horizon
// is not Waypoint-specific. But we needed this done before launch so here we
// are making technical debt for ourselves.
package httpassets

//go:generate go-bindata -pkg httpassets -fs -prefix "static/" static/...
