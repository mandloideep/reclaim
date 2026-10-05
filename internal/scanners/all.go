// Package scanners is the explicit registry of every scanner reclaim runs.
//
// Adding a scanner means adding it to All. There is no registration from init
// functions, so this file is the complete list.
package scanners

import (
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners/appcache"
	"github.com/mandloideep/reclaim/internal/scanners/caches"
	"github.com/mandloideep/reclaim/internal/scanners/docker"
	"github.com/mandloideep/reclaim/internal/scanners/downloads"
	"github.com/mandloideep/reclaim/internal/scanners/projects"
)

// Registry holds one fresh instance of every scanner.
type Registry struct {
	projects  []*projects.Scanner
	caches    []*caches.Scanner
	docker    docker.Scanner
	appcache  []*appcache.Scanner
	downloads []*downloads.Scanner
}

// New returns a registry with every scanner.
func New() *Registry {
	return &Registry{
		projects:  projects.Scanners(),
		caches:    caches.Scanners(),
		docker:    docker.Scanner{},
		appcache:  appcache.Scanners(),
		downloads: downloads.Scanners(),
	}
}

// All returns every scanner: project scanners first, then package caches,
// Docker, app caches and Downloads.
func (r *Registry) All() []scan.Scanner {
	out := make([]scan.Scanner, 0, len(r.projects)+len(r.caches)+1+len(r.appcache)+len(r.downloads))
	for _, s := range r.projects {
		out = append(out, s)
	}
	for _, s := range r.caches {
		out = append(out, s)
	}
	out = append(out, r.docker)
	for _, s := range r.appcache {
		out = append(out, s)
	}
	for _, s := range r.downloads {
		out = append(out, s)
	}
	return out
}

// ProjectMatchers returns every project scanner as a matcher, for the shared
// project walk. The walk always uses every matcher, even when only some
// project scanners run, so artifact folders are never descended into.
func (r *Registry) ProjectMatchers() []project.Matcher {
	return projects.Matchers(r.projects)
}
