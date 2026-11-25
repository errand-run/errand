// What a directory served as it is asks for ( its `services.json`, ADR 0026 ), and the question it makes of a machine.

package svc

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
)

type AskedService struct {
	Version   string         `json:"version"`
	Protocol  any            `json:"protocol"`
	Optional  bool           `json:"optional"`
	Text      string         `json:"text"`
	Package   map[string]any `json:"package"`
	Source    map[string]any `json:"source"`
	Described map[string]any `json:"described"`
}

func (s AskedService) Key(name string) string {
	if s.Version != "" {
		return name + "#" + s.Version
	}
	return name
}

// AskedServices: the services a directory names
func AskedServices(dir string) map[string]AskedService {
	out := map[string]AskedService{}
	data, err := os.ReadFile(filepath.Join(dir, "services.json"))
	var got map[string]any
	if err != nil || json.Unmarshal(data, &got) != nil {
		return out
	}
	services := got
	format, _ := got["format"].(float64)
	if s, ok := got["services"].(map[string]any); ok && format == 2 {
		services = s
	}
	for name, x := range services {
		v, _ := x.(map[string]any)
		if v == nil {
			continue
		}
		obj := func(k string) map[string]any {
			m, _ := v[k].(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			return m
		}
		one := AskedService{Protocol: v["protocol"], Optional: true, Package: map[string]any{}, Source: map[string]any{}, Described: map[string]any{}}
		if o, ok := v["optional"].(bool); ok {
			one.Optional = o
		}
		if format == 2 {
			one.Version, _ = v["version"].(string)
			one.Package, one.Source, one.Described = obj("package"), obj("source"), obj("described")
			one.Text, _ = v["text"].(string)
			if one.Text == "" {
				one.Text, _ = one.Described["text"].(string)
			}
		} else {
			one.Text, _ = obj("install")["text"].(string)
		}
		out[name] = one
	}
	return out
}

// LookRequest: what is asked of machine `m` about a directory's services
func LookRequest(dir, vid, m string) map[string]any {
	services := map[string]any{}
	for name, want := range AskedServices(dir) {
		services[name] = map[string]any{"version": OrNil(want.Version), "protocol": want.Protocol, "package": want.Package, "source": want.Source,
			"described": want.Described, "described_by": "store"}
	}
	var size int64
	files := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if st, err := d.Info(); err == nil {
				size += st.Size()
				files++
			}
		}
		return nil
	})
	check := [][]any{}
	for _, rel := range []string{"services.json", "index.html", "manifest.json"} {
		if st, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			check = append(check, []any{rel, st.Size()})
		}
	}
	store := map[string]any{"paths": []string{dir}, "original": false, "copy": nil, "check": check, "size": size, "files": files}
	if vid != "" {
		store["copy"] = "views/" + vid
	}
	return map[string]any{"do": "look", "machine": m, "services": services, "paths": map[string][]string{}, "store": store}
}

func OrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
