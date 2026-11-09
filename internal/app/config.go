package app

import (
	"errors"
	"os"
	"strings"
)

// Config comes from the environment (COVE_*), which Homeport's catalog entry fills in.
type Config struct {
	Data string // cove.db, secret.key, files/<user>, trash/<user>
	URL  string // the address people open, https://cove.example.com
	Addr string // where it listens

	// Homeport's OpenID Connect: everyone signs in with their Homeport account.
	Issuer, ClientID, ClientSecret string

	// Collabora Online's address, for opening documents ("" turns documents off).
	Collabora string
	// OfficeInternal is the built-in office: Collabora Online on the containers' network (http://office:9980),
	// reached through Cove's own address. Collabora then reads files at WOPIURL (http://app:8080), also inside.
	OfficeInternal, WOPIURL string

	// DevUser signs everyone in as this user, for trying Cove out without Homeport.
	DevUser string

	Version string
}

func ConfigFromEnv() (Config, error) {
	c := Config{
		Data:           env("COVE_DATA", "/data"),
		URL:            strings.TrimSuffix(os.Getenv("COVE_URL"), "/"),
		Addr:           env("COVE_ADDR", ":8080"),
		Issuer:         strings.TrimSuffix(os.Getenv("COVE_OIDC_ISSUER"), "/"),
		ClientID:       os.Getenv("COVE_OIDC_CLIENT_ID"),
		ClientSecret:   os.Getenv("COVE_OIDC_CLIENT_SECRET"),
		Collabora:      strings.TrimSuffix(os.Getenv("COVE_COLLABORA"), "/"),
		DevUser:        os.Getenv("COVE_DEV_USER"),
		OfficeInternal: strings.TrimSuffix(os.Getenv("COVE_OFFICE_INTERNAL"), "/"),
		WOPIURL:        strings.TrimSuffix(os.Getenv("COVE_WOPI_URL"), "/"),
	}
	if c.OfficeInternal != "" {
		c.Collabora = c.URL
	}
	return c, c.check()
}

func (c Config) check() error {
	switch {
	case c.URL == "":
		return errors.New("COVE_URL is required: the address people open, like https://cove.example.com")
	case c.Issuer == "" && c.DevUser == "":
		return errors.New("set COVE_OIDC_ISSUER, COVE_OIDC_CLIENT_ID and COVE_OIDC_CLIENT_SECRET (or COVE_DEV_USER to try Cove out)")
	case c.Issuer != "" && (c.ClientID == "" || c.ClientSecret == ""):
		return errors.New("COVE_OIDC_CLIENT_ID and COVE_OIDC_CLIENT_SECRET go with COVE_OIDC_ISSUER")
	case c.DevUser != "" && c.Issuer != "":
		return errors.New("COVE_DEV_USER is only for trying Cove out without sign-in; remove it when using COVE_OIDC_ISSUER")
	case c.DevUser != "" && !validUser(c.DevUser):
		return errors.New("COVE_DEV_USER isn't a valid user name")
	}
	return nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
