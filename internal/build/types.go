package build

import (
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type Nix2ContainerConfig struct {
	Version     int               `json:"version"`
	ImageConfig specs.ImageConfig `json:"image-config"`
	Layers      []Layer           `json:"layers"`
	Arch        string            `json:"arch"`
	Created     string            `json:"created"`
}

type Layer struct {
	Digest    string  `json:"digest"`
	Size      int64   `json:"size"`
	DiffIDs   string  `json:"diff_ids"`
	Paths     []Path  `json:"paths"`
	MediaType string  `json:"mediatype"`
	LayerPath *string `json:"layer-path"`
	History   History `json:"History"`
}

type Path struct {
	Path    string       `json:"path"`
	Options *PathOptions `json:"options,omitempty"`
}

type PathOptions struct {
	Rewrite *RewriteOptions `json:"rewrite"`
}

type RewriteOptions struct {
	Regex string `json:"regex"`
	Repl  string `json:"repl"`
}

type History struct {
	CreatedBy string `json:"created_by"`
}
