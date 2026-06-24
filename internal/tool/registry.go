package tool

import "github.com/local/swe-agent/internal/core"

type Registry struct {
	tools map[string]core.Tool
	order []string
}

func NewRegistry(enabled []string) *Registry {
	all := map[string]core.Tool{
		"shell":       Shell{},
		"read_file":   ReadFile{},
		"grep":        Grep{},
		"list_files":  ListFiles{},
		"git_diff":    GitDiff{},
		"apply_patch": ApplyPatch{},
		"run_tests":   RunTests{},
		"submit":      Submit{},
	}
	r := &Registry{tools: map[string]core.Tool{}}
	if len(enabled) == 0 {
		enabled = []string{"read_file", "grep", "list_files", "git_diff", "apply_patch", "shell", "run_tests", "submit"}
	}
	for _, name := range enabled {
		if t, ok := all[name]; ok {
			r.tools[name] = t
			r.order = append(r.order, name)
		}
	}
	return r
}

func (r *Registry) List() []core.ToolSpec {
	specs := make([]core.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.tools[name].Spec())
	}
	return specs
}

func (r *Registry) Get(name string) (core.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}
