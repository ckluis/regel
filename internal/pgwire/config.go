package pgwire

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Config is a resolved connection target.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	// RuntimeParams are extra startup parameters (application_name, etc.).
	RuntimeParams map[string]string
	// TLS, when true, attempts an SSLRequest negotiation before startup.
	TLS bool
}

func (c Config) address() string { return net.JoinHostPort(c.Host, c.Port) }

// ParseDSN parses a libpq-style URL: postgres://user:pass@host:port/db?opt=val
func ParseDSN(dsn string) (Config, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return Config{}, fmt.Errorf("pgwire: bad DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return Config{}, fmt.Errorf("pgwire: DSN scheme must be postgres:// (got %q)", u.Scheme)
	}
	cfg := Config{
		Host:          "localhost",
		Port:          "5432",
		RuntimeParams: map[string]string{},
	}
	if h := u.Hostname(); h != "" {
		cfg.Host = h
	}
	if p := u.Port(); p != "" {
		cfg.Port = p
	}
	if u.User != nil {
		cfg.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			cfg.Password = pw
		}
	}
	cfg.Database = strings.TrimPrefix(u.Path, "/")
	if cfg.Database == "" {
		cfg.Database = cfg.User
	}
	q := u.Query()
	for k := range q {
		v := q.Get(k)
		switch k {
		case "sslmode":
			switch v {
			case "disable", "":
				cfg.TLS = false
			case "prefer", "allow", "require", "verify-ca", "verify-full":
				cfg.TLS = true
			}
		case "dbname":
			cfg.Database = v
		case "user":
			if cfg.User == "" {
				cfg.User = v
			}
		case "password":
			if cfg.Password == "" {
				cfg.Password = v
			}
		default:
			cfg.RuntimeParams[k] = v
		}
	}
	if cfg.User == "" {
		return Config{}, fmt.Errorf("pgwire: DSN has no user")
	}
	if cfg.Database == "" {
		cfg.Database = cfg.User
	}
	return cfg, nil
}
