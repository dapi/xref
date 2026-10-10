package xrefgo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func indexPath(root string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "xref-go", digest([]byte(root))[:16]+".json"), nil
}

func loadIndex(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, err
	}
	if head.Version != indexVersion {
		return nil, nil
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	if idx.Version != indexVersion {
		return nil, fmt.Errorf("несовместимая версия индекса: %d", idx.Version)
	}
	return &idx, nil
}

func saveIndex(path string, idx *Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func trackedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range strings.Split(string(b), "\x00") {
		path := filepath.ToSlash(raw)
		if path == "" || seen[path] || skipCodePath(path) {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func skipCodePath(path string) bool {
	for _, p := range []string{"vendor/", "node_modules/", "public/", "tmp/", "log/", "coverage/", ".git/", ".bundle/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func scanExtension(path string) bool {
	switch filepath.Ext(path) {
	case ".rb", ".rake", ".ru", ".sql", ".erb", ".haml":
		return true
	}
	return false
}

func regularWithinRoot(root, rel string) (string, os.FileInfo, bool) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, false
	}
	abs := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, false
	}
	back, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", nil, false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	return resolved, info, true
}

func engineDigest() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return ""
	}
	return digest(b)
}

func refresh(root, path string, previous *Index, schemaPaths map[string]string) (*Index, int, error) {
	files, err := trackedFiles(root)
	if err != nil {
		return nil, 0, err
	}
	engine := engineDigest()
	idx := &Index{Version: indexVersion, EngineSHA: engine, Root: root, Updated: time.Now(), Files: map[string]File{}, Schemas: map[string]Schema{}, ModelMap: map[string]ModelInfo{}}
	if previous != nil && previous.Root != root {
		return nil, 0, fmt.Errorf("индекс принадлежит другому проекту")
	}
	if previous != nil && len(schemaPaths) == 0 {
		for name, s := range previous.Schemas {
			if !strings.HasPrefix(s.Path, "pg:") {
				schemaPaths[name] = s.Path
			}
		}
	}
	for name, sp := range schemaPaths {
		var s Schema
		if strings.HasPrefix(sp, "pg:") {
			s, err = parseLiveSchema(strings.TrimPrefix(sp, "pg:"))
		} else {
			absolute := sp
			if !filepath.IsAbs(sp) {
				absolute = filepath.Join(root, sp)
			}
			s, err = parseSchema(absolute)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("схема %s: %w", name, err)
		}
		idx.Schemas[name] = s
	}
	known := map[string]bool{}
	for _, s := range idx.Schemas {
		for table := range s.Tables {
			known[table] = true
		}
	}
	fileData := map[string][]byte{}
	fileStats := map[string]os.FileInfo{}
	filePaths := map[string]string{}
	ambiguous := map[string]bool{}
	for _, rel := range files {
		if !scanExtension(rel) || strings.HasPrefix(rel, "db/") && strings.HasSuffix(rel, "structure.sql") {
			continue
		}
		abs, info, ok := regularWithinRoot(root, rel)
		if !ok {
			continue
		}
		if info.Size() > 8*1024*1024 {
			continue
		}
		fileStats[rel] = info
		filePaths[rel] = abs
		if strings.HasPrefix(rel, "app/models/") && strings.HasSuffix(rel, ".rb") {
			b, err := os.ReadFile(abs)
			if err == nil {
				fileData[rel] = b
				for k, v := range modelDeclarations(rel, b, known) {
					if ambiguous[k] {
						continue
					}
					if prior, exists := idx.ModelMap[k]; exists && prior.Table != v.Table {
						delete(idx.ModelMap, k)
						ambiguous[k] = true
						continue
					}
					if prior, exists := idx.ModelMap[k]; exists {
						v.Explicit = v.Explicit || prior.Explicit
						v.Verified = v.Verified || prior.Verified
					}
					idx.ModelMap[k] = v
				}
			}
		}
	}
	idx.AmbiguousModels = keys(ambiguous)
	modelChanged := previous == nil || previous.EngineSHA != engine || !sameModels(previous.ModelMap, idx.ModelMap)
	schemaChanged := previous == nil || !sameSchemas(previous.Schemas, idx.Schemas)
	changed := 0
	for _, rel := range files {
		info, ok := fileStats[rel]
		if !ok {
			continue
		}
		if previous != nil && !modelChanged && !schemaChanged {
			if old, ok := previous.Files[rel]; ok && old.Size == info.Size() && old.ModTime == info.ModTime().UnixNano() {
				idx.Files[rel] = old
				continue
			}
		}
		b, ok := fileData[rel]
		if !ok {
			b, err = os.ReadFile(filePaths[rel])
			if err != nil {
				continue
			}
		}
		sha := digest(b)
		if previous != nil && !modelChanged && !schemaChanged {
			if old, ok := previous.Files[rel]; ok && old.SHA256 == sha {
				old.ModTime = info.ModTime().UnixNano()
				idx.Files[rel] = old
				continue
			}
		}
		var refs []Ref
		var methods []Method
		var calls []Call
		var routes []Route
		var renders []Render
		var locals []LocalBinding
		parseErrors := 0
		segments := 0
		if strings.HasSuffix(rel, ".sql") {
			appendSQL(&refs, rel, 1, string(b))
		} else if strings.HasSuffix(rel, ".erb") || strings.HasSuffix(rel, ".haml") {
			refs, parseErrors, segments = scanTemplate(rel, b, idx.ModelMap)
		} else {
			refs, parseErrors = scanRuby(rel, b, idx.ModelMap)
			methods, calls, routes, renders, locals = scanGraph(rel, b)
			scopeMethods, scopeRefs := scanRailsScopes(rel, b, idx.ModelMap)
			methods = append(methods, scopeMethods...)
			refs = dedup(append(refs, scopeRefs...))
		}
		idx.Files[rel] = File{Size: info.Size(), ModTime: info.ModTime().UnixNano(), SHA256: sha, Refs: refs, Errors: parseErrors, Segments: segments, Methods: methods, Calls: calls, Routes: routes, Renders: renders, Locals: locals, Includes: scanModelIncludes(rel, b, idx.ModelMap), ConcernScopes: scanConcernScopes(rel, b)}
		changed++
	}
	for _, f := range idx.Files {
		for _, r := range f.Refs {
			if r.Confidence == "blind" {
				idx.BlindSpots++
			}
		}
	}
	if err := saveIndex(path, idx); err != nil {
		return nil, 0, err
	}
	return idx, changed, nil
}

func sameModels(a, b map[string]ModelInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func sameSchemas(a, b map[string]Schema) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v.SHA256 != w.SHA256 {
			return false
		}
	}
	return true
}
