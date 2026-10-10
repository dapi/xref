package xrefgo

import "time"

const indexVersion = 9

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
	Size     int64          `json:"size"`
	ModTime  int64          `json:"mtime"`
	SHA256   string         `json:"sha256"`
	Refs     []Ref          `json:"refs,omitempty"`
	Errors   int            `json:"errors,omitempty"`
	Segments int            `json:"template_segments,omitempty"`
	Methods  []Method       `json:"methods,omitempty"`
	Calls    []Call         `json:"calls,omitempty"`
	Routes   []Route        `json:"routes,omitempty"`
	Renders  []Render       `json:"renders,omitempty"`
	Locals   []LocalBinding `json:"locals,omitempty"`
}

type Method struct {
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Class   bool   `json:"class,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Kind    string `json:"kind,omitempty"`
}

type Call struct {
	Owner        string `json:"owner"`
	Source       string `json:"source"`
	SourceClass  bool   `json:"source_class,omitempty"`
	Receiver     string `json:"receiver,omitempty"`
	ReceiverKind string `json:"receiver_kind,omitempty"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Direct       bool   `json:"direct,omitempty"`
}

type LocalBinding struct {
	Owner       string `json:"owner"`
	Source      string `json:"source"`
	SourceClass bool   `json:"source_class,omitempty"`
	Name        string `json:"name"`
	Target      string `json:"target,omitempty"`
	Class       bool   `json:"class,omitempty"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
}

type Route struct {
	Verb       string `json:"verb"`
	Path       string `json:"path"`
	Controller string `json:"controller"`
	Action     string `json:"action"`
	Source     string `json:"source"`
	Line       int    `json:"line"`
	Confidence string `json:"confidence"`
}

type Render struct {
	Owner       string `json:"owner"`
	Source      string `json:"source"`
	SourceClass bool   `json:"source_class,omitempty"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Kind        string `json:"kind"`
	Target      string `json:"target,omitempty"`
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
