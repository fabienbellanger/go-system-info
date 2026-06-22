# syntax=docker/dockerfile:1

# ---------- Étape de build ----------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Téléchargement des dépendances en couche séparée (cache tant que go.* ne change pas).
COPY go.mod go.sum ./
RUN go mod download

# Code source (l'interface web de public/ est embarquée via go:embed).
COPY . .

# Binaire statique (CGO désactivé) ; la version est injectable au build.
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /app .

# ---------- Image finale ----------
# scratch : aucune dépendance runtime (binaire statique, aucun appel réseau externe).
FROM scratch
COPY --from=build /app /app
EXPOSE 8222
ENTRYPOINT ["/app"]
# Surcharge possible : docker run -p 9090:9090 IMAGE -p 9090 -r 5s
