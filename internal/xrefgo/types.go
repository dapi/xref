package xrefgo

import "time"

const indexVersion = 5

// Evidence deliberately separates parsed facts from inferred ORM uses and text.
type Ref struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Offset     int    `json:"offset,omitempty"`
	Kind       string `json:"kind"`
	Table      string `json:"table,omitempty"`
	Column     string `json:"column,omitempty"`
	Confidence string `json:"confidence"`
	Detail     string `json:"detail,omitempty"`
}

type File struct {
	Size     int64  `json:"size"`
	ModTime  int64  `json:"mtime"`
	SHA256   string `json:"sha256"`
	Refs     []Ref  `json:"refs,omitempty"`
	Errors   int    `json:"errors,omitempty"`
	Segments int    `json:"template_segments,omitempty"`
}

type Schema struct {
	Path       string              `json:"path"`
	SHA256     string              `json:"sha256"`
	Tables     map[string][]string `json:"tables"`
	ParseError int                 `json:"parse_errors"`
}

type ModelInfo struct {
	Table    string `json:"table"`
	Explicit bool   `json:"explicit"`
	Verified bool   `json:"verified"`
}

type Index struct {
	Version         int                  `json:"version"`
	EngineSHA       string               `json:"engine_sha"`
	Root            string               `json:"root"`
	Updated         time.Time            `json:"updated"`
	Files           map[string]File      `json:"files"`
	Schemas         map[string]Schema    `json:"schemas"`
	ModelMap        map[string]ModelInfo `json:"model_map"`
	AmbiguousModels []string             `json:"ambiguous_models,omitempty"`
	BlindSpots      int                  `json:"blind_spots"`
}
