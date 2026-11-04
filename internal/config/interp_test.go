package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupFrom(m map[string]string) EnvLookup {
	return func(name string) (string, bool) { v, ok := m[name]; return v, ok }
}

func TestExpandEnv(t *testing.T) {
	env := lookupFrom(map[string]string{"HOST": "h", "EMPTY": ""})
	tests := []struct {
		name           string
		in             string
		strict         bool
		want           string
		wantUnresolved []string
		wantErr        string
	}{
		{name: "set", in: "http://${HOST}/x", want: "http://h/x"},
		{name: "set to empty", in: "a${EMPTY}b", want: "ab"},
		{name: "default used", in: "${PORT:-8080}", want: "8080"},
		{name: "default ignored when set", in: "${HOST:-other}", want: "h"},
		{name: "empty default", in: "${PORT:-}", want: ""},
		{name: "unset strict", in: "${MISSING}", strict: true, want: "${MISSING}", wantErr: "env var MISSING is not set"},
		{name: "unset lenient", in: "${MISSING}/${MISSING}/${OTHER}", want: "${MISSING}/${MISSING}/${OTHER}", wantUnresolved: []string{"MISSING", "OTHER"}},
		{name: "escaped", in: "$${HOST}", want: "${HOST}"},
		{name: "plain", in: "no vars", want: "no vars"},
		{name: "not a var", in: "${1BAD}", want: "${1BAD}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unresolved, err := ExpandEnv(tt.in, env, tt.strict)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantUnresolved, unresolved)
		})
	}
}

func TestRender(t *testing.T) {
	sc := Scope{
		Vars:    map[string]string{"user": "qa@example.com"},
		Env:     lookupFrom(map[string]string{"REGION": "eu"}),
		Secrets: func(name string) (string, bool) { return "s3cret", name == "TOKEN" },
		Values:  map[string]string{"session": "sid", "message": "hello", "sip.inbound_number": "+34910000000"},
		Now:     func() time.Time { return time.Date(2026, 10, 31, 14, 20, 0, 0, time.UTC) },
		UUID:    func() string { return "00000000-0000-4000-8000-000000000000" },
	}
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "uuid", in: "room-{{uuid}}", want: "room-00000000-0000-4000-8000-000000000000"},
		{name: "now", in: "{{now}}", want: "2026-10-31T14:20:00Z"},
		{name: "env", in: "{{env.REGION}}", want: "eu"},
		{name: "env missing", in: "{{env.NOPE}}", wantErr: "env var NOPE is not set"},
		{name: "vars", in: "Log in as {{vars.user}}", want: "Log in as qa@example.com"},
		{name: "vars missing", in: "{{vars.nope}}", wantErr: "var nope is not defined"},
		{name: "secret", in: "Bearer {{secrets.TOKEN}}", want: "Bearer s3cret"},
		{name: "secret missing", in: "{{secrets.OTHER}}", wantErr: "secret OTHER is not resolved"},
		{name: "values", in: `{"session_id": "{{ session }}", "message": "{{message}}"}`, want: `{"session_id": "sid", "message": "hello"}`},
		{name: "dotted value", in: "{{sip.inbound_number}}", want: "+34910000000"},
		{name: "unknown", in: "{{nope}}", wantErr: "unknown template {{nope}}"},
		{name: "unknown faker", in: "{{faker.ssn}}", wantErr: "unknown template {{faker.ssn}} (have email, name, phone, uuid, number)"},
		{name: "plain", in: "no templates ${X}", want: "no templates ${X}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.in, sc)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderFaker(t *testing.T) {
	email, err := Render("{{faker.email}}", Scope{})
	require.NoError(t, err)
	assert.Regexp(t, `^qa-[0-9a-f]{8}@example\.com$`, email)
	phone, err := Render("{{faker.phone}}", Scope{})
	require.NoError(t, err)
	assert.Regexp(t, `^\+1555\d{7}$`, phone)
	custom, err := Render("{{faker.city}}", Scope{Faker: func(kind string) (string, bool) { return "Lleida", kind == "city" }})
	require.NoError(t, err)
	assert.Equal(t, "Lleida", custom)
}

func TestSecretRefs(t *testing.T) {
	assert.Equal(t, []string{"A", "B"}, SecretRefs("{{secrets.A}} {{ secrets.B }} {{secrets.A}} {{vars.x}}"))
	assert.Nil(t, SecretRefs("nothing here"))
	assert.Equal(t, []string{"secrets.A", "vars.x", "uuid"}, TemplateRefs("{{secrets.A}}{{vars.x}}{{uuid}}{{vars.x}}"))
}

func TestIsSecretField(t *testing.T) {
	tests := []struct {
		name string
		path []string
		want bool
	}{
		{"api_key", []string{"targets", "voice", "api_key"}, true},
		{"api_secret", []string{"targets", "voice", "api_secret"}, true},
		{"authorization header", []string{"targets", "api", "headers", "Authorization"}, true},
		{"authorization elsewhere", []string{"vars", "Authorization"}, false},
		{"password", []string{"password"}, true},
		{"token", []string{"x", "token"}, true},
		{"url", []string{"targets", "api", "url"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSecretField(tt.path))
		})
	}
}
