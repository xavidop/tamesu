package config

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EnvLookup reads one environment variable, reporting whether it is set.
type EnvLookup func(name string) (string, bool)

var (
	// ${VAR} and ${VAR:-default}; a leading $$ escapes the expansion.
	envRe = regexp.MustCompile(`\$\$?\{([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)
	// {{ name }} template placeholders: uuid, now, env.X, vars.x, secrets.X,
	// faker.kind and the run-time values (session, message, sip.inbound_number).
	tmplRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}`)
)

// ExpandEnv replaces ${VAR} and ${VAR:-default} in s. A variable that is not
// set and has no default is an error when strict is true; otherwise the
// placeholder is kept and the name is returned in unresolved. $${X} is the
// literal ${X}.
func ExpandEnv(s string, lookup EnvLookup, strict bool) (out string, unresolved []string, err error) {
	if !strings.Contains(s, "${") {
		return s, nil, nil
	}
	out = envRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "$$") {
			return m[1:]
		}
		sub := envRe.FindStringSubmatch(m)
		name, def := sub[1], sub[2]
		if lookup != nil {
			if v, ok := lookup(name); ok {
				return v
			}
		}
		if def != "" {
			return strings.TrimPrefix(def, ":-")
		}
		if strict {
			if err == nil {
				err = fmt.Errorf("env var %s is not set", name)
			}
			return m
		}
		if !slices.Contains(unresolved, name) {
			unresolved = append(unresolved, name)
		}
		return m
	})
	return out, unresolved, err
}

// Scope supplies the values a template may use.
type Scope struct {
	// Vars backs {{vars.x}}.
	Vars map[string]string
	// Env backs {{env.X}}.
	Env EnvLookup
	// Secrets backs {{secrets.X}}; it returns the revealed value.
	Secrets func(name string) (string, bool)
	// Values are the run-time placeholders such as session, message and
	// sip.inbound_number. They win over every other name.
	Values map[string]string
	// Now backs {{now}}; nil means time.Now.
	Now func() time.Time
	// UUID backs {{uuid}}; nil means a random UUID v4.
	UUID func() string
	// Faker backs {{faker.kind}} before the built-in kinds email, name, phone
	// and uuid.
	Faker func(kind string) (string, bool)
}

// Render replaces every {{...}} placeholder in s from sc. A placeholder that
// cannot be resolved is an error; nothing else in s is touched.
func Render(s string, sc Scope) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	var err error
	out := tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		if err != nil {
			return m
		}
		key := tmplRe.FindStringSubmatch(m)[1]
		v, e := sc.lookup(key)
		if e != nil {
			err = e
			return m
		}
		return v
	})
	return out, err
}

func (sc Scope) lookup(key string) (string, error) {
	if v, ok := sc.Values[key]; ok {
		return v, nil
	}
	switch key {
	case "uuid":
		if sc.UUID != nil {
			return sc.UUID(), nil
		}
		return uuid.NewString(), nil
	case "now":
		now := time.Now
		if sc.Now != nil {
			now = sc.Now
		}
		return now().UTC().Format(time.RFC3339), nil
	}
	if name, ok := strings.CutPrefix(key, "env."); ok {
		if sc.Env != nil {
			if v, ok := sc.Env(name); ok {
				return v, nil
			}
		}
		return "", fmt.Errorf("env var %s is not set", name)
	}
	if name, ok := strings.CutPrefix(key, "vars."); ok {
		if v, ok := sc.Vars[name]; ok {
			return v, nil
		}
		return "", fmt.Errorf("var %s is not defined", name)
	}
	if name, ok := strings.CutPrefix(key, "secrets."); ok {
		if sc.Secrets != nil {
			if v, ok := sc.Secrets(name); ok {
				return v, nil
			}
		}
		return "", fmt.Errorf("secret %s is not resolved", name)
	}
	if kind, ok := strings.CutPrefix(key, "faker."); ok {
		if sc.Faker != nil {
			if v, ok := sc.Faker(kind); ok {
				return v, nil
			}
		}
		return fake(kind)
	}
	return "", fmt.Errorf("unknown template {{%s}}", key)
}

var fakeNames = []string{"Alice Martin", "Jordi Puig", "Marta Soler", "Kenji Sato", "Amara Okafor", "Lucas Meyer", "Noor Haddad", "Sofia Rossi"}

func fake(kind string) (string, error) {
	switch kind {
	case "email":
		return fmt.Sprintf("qa-%08x@example.com", rand.Uint32()), nil
	case "name":
		return fakeNames[rand.IntN(len(fakeNames))], nil
	case "phone":
		return fmt.Sprintf("+1555%07d", rand.IntN(10_000_000)), nil
	case "uuid":
		return uuid.NewString(), nil
	}
	return "", fmt.Errorf("unknown template {{faker.%s}} (have email, name, phone, uuid)", kind)
}

// TemplateRefs lists every placeholder key in s, deduplicated, in order.
func TemplateRefs(s string) []string {
	var out []string
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// SecretRefs lists the names used as {{secrets.NAME}} in s, deduplicated, in order.
func SecretRefs(s string) []string {
	var out []string
	for _, key := range TemplateRefs(s) {
		if name, ok := strings.CutPrefix(key, "secrets."); ok && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// SecretFieldNames are the fields documented as secrets: a ${VAR} in one of
// them earns a warning, because ${VAR} is for non-secret values.
var SecretFieldNames = []string{"api_key", "api_secret", "Authorization", "password", "token"}

// IsSecretField reports whether a YAML path ends in a secret field
// (api_key, api_secret, headers.Authorization, password, token).
func IsSecretField(path []string) bool {
	if len(path) == 0 {
		return false
	}
	last := path[len(path)-1]
	if last == "Authorization" {
		return len(path) >= 2 && path[len(path)-2] == "headers"
	}
	return slices.Contains(SecretFieldNames, last)
}
