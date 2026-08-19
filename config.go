package main

import (
	"os"
	"strings"
)

type Config struct {
	Addr        string
	DatabaseURL string
	SecretKey   string
	APIToken    string
	UserEmail   string
}

func loadConfig() *Config {
	// PORT wins: teploy injects the container port the yml declares, as a
	// bare number; ListenAndServe wants host:port.
	addr := envOr("ADDR", ":8080")
	if p := osGetenv("PORT"); p != "" {
		if !strings.Contains(p, ":") {
			p = ":" + p
		}
		addr = p
	}
	return &Config{
		Addr:        addr,
		DatabaseURL: os.Getenv("DATABASE_URL"),
		SecretKey:   strings.TrimSpace(os.Getenv("SECRET_KEY")),
		APIToken:    strings.TrimSpace(os.Getenv("EMAILSOFT_TOKEN")),
		UserEmail:   strings.TrimSpace(os.Getenv("EMAILSOFT_USER_EMAIL")),
	}
}
