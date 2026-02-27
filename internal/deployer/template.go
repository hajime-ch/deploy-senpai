package deployer

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/hajime-ch/deploy-senpai/internal/config"
)

// TemplateData holds the variables available in user compose templates.
type TemplateData struct {
	AppName         string
	Branch          string
	SanitizedBranch string
	ImageTag        string
	Image           string // full image reference, e.g. ghcr.io/org/repo:tag
	Registry        string
	Owner           string
	BaseDomain      string
	Subdomain       string
	Network         string
	Env             map[string]string
}

// TemplateRenderer renders user-provided compose templates.
type TemplateRenderer struct{}

// NewTemplateRenderer creates a new TemplateRenderer.
func NewTemplateRenderer() *TemplateRenderer {
	return &TemplateRenderer{}
}

// RenderTemplate reads templatePath, renders it with data and a {{password "name"}}
// function, and writes the result to outPath. existingPasswords supplies previously
// generated passwords so re-renders are deterministic. Returns the full passwords
// map (existing + any newly generated).
func (tr *TemplateRenderer) RenderTemplate(templatePath string, data TemplateData, existingPasswords map[string]string, outPath string) (map[string]string, error) {
	tplBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", templatePath, err)
	}

	passwords := make(map[string]string)
	for k, v := range existingPasswords {
		passwords[k] = v
	}

	funcMap := template.FuncMap{
		"password": func(name string) (string, error) {
			if pw, ok := passwords[name]; ok {
				return pw, nil
			}
			pw, err := config.GeneratePassword()
			if err != nil {
				return "", fmt.Errorf("generating password %q: %w", name, err)
			}
			passwords[name] = pw
			return pw, nil
		},
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Funcs(funcMap).Parse(string(tplBytes))
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("creating output file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, data); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}

	return passwords, nil
}
