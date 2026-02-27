package deployer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTemplate_VariableInjection(t *testing.T) {
	tmpDir := t.TempDir()

	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	tplContent := `services:
  app:
    image: {{.Image}}
    container_name: {{.AppName}}-{{.SanitizedBranch}}
    networks:
      - {{.Network}}
    environment:
      BASE_DOMAIN: {{.BaseDomain}}
      SUBDOMAIN: {{.Subdomain}}
{{- range $key, $value := .Env}}
      {{$key}}: "{{$value}}"
{{- end}}
`
	if err := os.WriteFile(tplPath, []byte(tplContent), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(tmpDir, "docker-compose.yml")
	renderer := NewTemplateRenderer()

	data := TemplateData{
		AppName:         "myapp",
		Branch:          "feature/test",
		SanitizedBranch: "feature-test",
		ImageTag:        "abc1234",
		Image:           "ghcr.io/org/myapp:abc1234",
		Registry:        "ghcr.io",
		Owner:           "org",
		BaseDomain:      "staging.example.com",
		Subdomain:       "myapp-feature-test",
		Network:         "web",
		Env:             map[string]string{"NODE_ENV": "staging"},
	}

	passwords, err := renderer.RenderTemplate(tplPath, data, nil, outPath)
	if err != nil {
		t.Fatalf("RenderTemplate() error: %v", err)
	}

	if len(passwords) != 0 {
		t.Errorf("expected no passwords, got %d", len(passwords))
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}

	output := string(content)
	if !strings.Contains(output, "ghcr.io/org/myapp:abc1234") {
		t.Error("output should contain image reference")
	}
	if !strings.Contains(output, "myapp-feature-test") {
		t.Error("output should contain container name")
	}
	if !strings.Contains(output, "staging.example.com") {
		t.Error("output should contain base domain")
	}
	if !strings.Contains(output, `NODE_ENV: "staging"`) {
		t.Error("output should contain env var")
	}
}

func TestRenderTemplate_PasswordFunction(t *testing.T) {
	tmpDir := t.TempDir()

	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	tplContent := `services:
  db:
    environment:
      POSTGRES_PASSWORD: "{{password "dbpass"}}"
  cache:
    environment:
      REDIS_PASSWORD: "{{password "redispass"}}"
`
	if err := os.WriteFile(tplPath, []byte(tplContent), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(tmpDir, "docker-compose.yml")
	renderer := NewTemplateRenderer()

	data := TemplateData{
		AppName:         "myapp",
		SanitizedBranch: "main",
	}

	passwords, err := renderer.RenderTemplate(tplPath, data, nil, outPath)
	if err != nil {
		t.Fatalf("RenderTemplate() error: %v", err)
	}

	// Should have generated two passwords
	if len(passwords) != 2 {
		t.Fatalf("expected 2 passwords, got %d", len(passwords))
	}

	dbPass, ok := passwords["dbpass"]
	if !ok {
		t.Fatal("missing 'dbpass' password")
	}
	if len(dbPass) != 32 {
		t.Errorf("dbpass length = %d, want 32", len(dbPass))
	}

	redisPass, ok := passwords["redispass"]
	if !ok {
		t.Fatal("missing 'redispass' password")
	}
	if len(redisPass) != 32 {
		t.Errorf("redispass length = %d, want 32", len(redisPass))
	}

	if dbPass == redisPass {
		t.Error("dbpass and redispass should be different")
	}

	// Verify passwords appear in output
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(content)
	if !strings.Contains(output, dbPass) {
		t.Error("output should contain dbpass value")
	}
	if !strings.Contains(output, redisPass) {
		t.Error("output should contain redispass value")
	}
}

func TestRenderTemplate_PasswordDeterminism(t *testing.T) {
	tmpDir := t.TempDir()

	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	tplContent := `PASS: "{{password "dbpass"}}"`
	if err := os.WriteFile(tplPath, []byte(tplContent), 0644); err != nil {
		t.Fatal(err)
	}

	renderer := NewTemplateRenderer()
	data := TemplateData{AppName: "app", SanitizedBranch: "main"}

	// First render: generates a new password
	outPath1 := filepath.Join(tmpDir, "out1.yml")
	passwords1, err := renderer.RenderTemplate(tplPath, data, nil, outPath1)
	if err != nil {
		t.Fatalf("first render error: %v", err)
	}

	// Second render: pass existing passwords, should reuse them
	outPath2 := filepath.Join(tmpDir, "out2.yml")
	passwords2, err := renderer.RenderTemplate(tplPath, data, passwords1, outPath2)
	if err != nil {
		t.Fatalf("second render error: %v", err)
	}

	if passwords1["dbpass"] != passwords2["dbpass"] {
		t.Errorf("password changed across renders: %q != %q", passwords1["dbpass"], passwords2["dbpass"])
	}

	// Verify both outputs are identical
	content1, _ := os.ReadFile(outPath1)
	content2, _ := os.ReadFile(outPath2)
	if string(content1) != string(content2) {
		t.Error("re-rendered output should be identical when passwords are preserved")
	}
}

func TestRenderTemplate_PreExistingPasswords(t *testing.T) {
	tmpDir := t.TempDir()

	tplPath := filepath.Join(tmpDir, "compose.yml.tpl")
	tplContent := `PASS: "{{password "mypass"}}"`
	if err := os.WriteFile(tplPath, []byte(tplContent), 0644); err != nil {
		t.Fatal(err)
	}

	renderer := NewTemplateRenderer()
	data := TemplateData{AppName: "app", SanitizedBranch: "main"}

	existing := map[string]string{"mypass": "fixed-password-value"}

	outPath := filepath.Join(tmpDir, "out.yml")
	passwords, err := renderer.RenderTemplate(tplPath, data, existing, outPath)
	if err != nil {
		t.Fatalf("RenderTemplate() error: %v", err)
	}

	if passwords["mypass"] != "fixed-password-value" {
		t.Errorf("password = %q, want %q", passwords["mypass"], "fixed-password-value")
	}

	content, _ := os.ReadFile(outPath)
	if !strings.Contains(string(content), "fixed-password-value") {
		t.Error("output should contain the pre-existing password value")
	}
}

func TestRenderTemplate_InvalidTemplate(t *testing.T) {
	tmpDir := t.TempDir()

	tplPath := filepath.Join(tmpDir, "bad.yml.tpl")
	if err := os.WriteFile(tplPath, []byte(`{{.Invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	renderer := NewTemplateRenderer()
	data := TemplateData{AppName: "app"}

	outPath := filepath.Join(tmpDir, "out.yml")
	_, err := renderer.RenderTemplate(tplPath, data, nil, outPath)
	if err == nil {
		t.Fatal("expected error for invalid template, got nil")
	}
}

func TestRenderTemplate_MissingTemplate(t *testing.T) {
	renderer := NewTemplateRenderer()
	data := TemplateData{AppName: "app"}

	_, err := renderer.RenderTemplate("/nonexistent/template.tpl", data, nil, "/tmp/out.yml")
	if err == nil {
		t.Fatal("expected error for missing template, got nil")
	}
}
