services:
  app:
    image: {{.Image}}
    container_name: {{.AppName}}-{{.SanitizedBranch}}-app
    restart: unless-stopped
    networks:
      - {{.Network}}
    environment:
      BASE_URL: "https://{{.Subdomain}}.{{.BaseDomain}}"
{{- range $key, $value := .Env}}
      {{$key}}: "{{$value}}"
{{- end}}
    labels:
      - "deployer.app={{.AppName}}"
      - "deployer.branch={{.SanitizedBranch}}"
      - "deployer.managed=true"

networks:
  {{.Network}}:
    external: true
