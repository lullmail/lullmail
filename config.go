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
	return &Config{
		// PORT wins: teploy injects the container port the yml declares.
		Addr:        envOr("PORT", envOr("ADDR", ":8080")),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		SecretKey:   strings.TrimSpace(os.Getenv("SECRET_KEY")),
		APIToken:    strings.TrimSpace(os.Getenv("EMAILSOFT_TOKEN")),
		UserEmail:   strings.TrimSpace(os.Getenv("EMAILSOFT_USER_EMAIL")),
	}
}
