package auth

import (
	"net/http"
	"strings"
)

type Auth struct{ keys map[string]struct{} }

func New(keys []string) *Auth {
	m := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			m[key] = struct{}{}
		}
	}
	return &Auth{keys: m}
}

func (a *Auth) Allowed(r *http.Request) bool {
	if a == nil || len(a.keys) == 0 {
		return true
	}
	candidates := []string{bearerToken(r.Header.Get("Authorization")), r.Header.Get("x-api-key"), r.Header.Get("api-key")}
	for _, token := range candidates {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if _, ok := a.keys[token]; ok {
			return true
		}
	}
	return false
}

func bearerToken(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return fields[1]
	}
	return ""
}
