services:
  app:
    image: {{.Image}}
    container_name: {{.AppName}}-{{.SanitizedBranch}}-app
    restart: unless-stopped
    networks:
      - {{.Network}}
      - internal
    environment:
      DATABASE_URL: "postgres://app:{{password "dbpass"}}@{{.AppName}}-{{.SanitizedBranch}}-db:5432/app?sslmode=disable"
      DB_HOST: "{{.AppName}}-{{.SanitizedBranch}}-db"
      DB_USER: "app"
      DB_PASSWORD: "{{password "dbpass"}}"
      DB_NAME: "app"
      BASE_URL: "https://{{.Subdomain}}.{{.BaseDomain}}"
{{- range $key, $value := .Env}}
      {{$key}}: "{{$value}}"
{{- end}}
    labels:
      - "deployer.app={{.AppName}}"
      - "deployer.branch={{.SanitizedBranch}}"
      - "deployer.managed=true"
    depends_on:
      db:
        condition: service_healthy

  db:
    image: postgres:16-alpine
    container_name: {{.AppName}}-{{.SanitizedBranch}}-db
    restart: unless-stopped
    networks:
      - internal
    environment:
      POSTGRES_USER: "app"
      POSTGRES_PASSWORD: "{{password "dbpass"}}"
      POSTGRES_DB: "app"
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app -d app"]
      interval: 5s
      timeout: 5s
      retries: 10
    labels:
      - "deployer.app={{.AppName}}"
      - "deployer.branch={{.SanitizedBranch}}"
      - "deployer.managed=true"

networks:
  {{.Network}}:
    external: true
  internal:
    driver: bridge

volumes:
  db-data:
