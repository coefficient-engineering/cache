package rules

import "crypto/tls"

func insecure() *tls.Config {
	// ruleid: tls-insecure-skip-verify
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
}

func insecureAssign(cfg *tls.Config) {
	// ruleid: tls-insecure-skip-verify
	cfg.InsecureSkipVerify = true
}

func secure() *tls.Config {
	// ok: tls-insecure-skip-verify
	return &tls.Config{MinVersion: tls.VersionTLS12}
}
