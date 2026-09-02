package main

import (
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Addr                  string
	DatabaseURL           string
	SecretKey             string
	APIToken              string
	UserEmail             string
	PublicURL             string
	PublicURLSet          bool
	RPID                  string
	SecureAuth            bool
	DataDir               string
	VAPIDPublic           string
	VAPIDPrivate          string
	VAPIDSubject          string
	GoogleClientID        string
	GoogleClientSecret    string
	MicrosoftClientID     string
	MicrosoftClientSecret string
	MicrosoftTenant       string
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
	// Origin: PUBLIC_URL pins it. Without it (and without WEBAUTHN_RP_ID),
	// the app boots in first-run setup mode and adopts the origin of the
	// browser request that completes setup — request detection replaces any
	// localhost guess, which would otherwise bind passkeys to the wrong RP ID.
	publicURL := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	publicURLSet := publicURL != ""
	rpID := strings.TrimSpace(os.Getenv("WEBAUTHN_RP_ID"))
	secure := false
	if publicURL == "" && rpID == "" {
		publicURL = ""
	} else if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	if parsed, err := url.Parse(publicURL); err == nil && parsed.Hostname() != "" {
		secure = parsed.Scheme == "https"
		if rpID == "" {
			rpID = parsed.Hostname()
		}
	}
	ownerEmail := strings.TrimSpace(os.Getenv("LULL_USER_EMAIL"))
	vapidSubject := strings.TrimSpace(os.Getenv("VAPID_SUBJECT"))
	if vapidSubject == "" && ownerEmail != "" {
		vapidSubject = "mailto:" + ownerEmail
	}
	return &Config{
		Addr:                  addr,
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		SecretKey:             strings.TrimSpace(os.Getenv("SECRET_KEY")),
		APIToken:              strings.TrimSpace(os.Getenv("LULL_TOKEN")),
		UserEmail:             ownerEmail,
		PublicURL:             publicURL,
		PublicURLSet:          publicURLSet,
		RPID:                  rpID,
		SecureAuth:            secure,
		DataDir:               strings.TrimRight(envOr("DATA_DIR", "./data"), "/"),
		VAPIDPublic:           strings.TrimSpace(os.Getenv("VAPID_PUBLIC_KEY")),
		VAPIDPrivate:          strings.TrimSpace(os.Getenv("VAPID_PRIVATE_KEY")),
		VAPIDSubject:          vapidSubject,
		GoogleClientID:        strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
		GoogleClientSecret:    strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")),
		MicrosoftClientID:     strings.TrimSpace(os.Getenv("MICROSOFT_CLIENT_ID")),
		MicrosoftClientSecret: strings.TrimSpace(os.Getenv("MICROSOFT_CLIENT_SECRET")),
		MicrosoftTenant:       envOr("MICROSOFT_TENANT", "common"),
	}
}
