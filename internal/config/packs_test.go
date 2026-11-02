package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinPacksAreValid(t *testing.T) {
	packs := BuiltinPacks()
	var names []string
	for _, p := range packs {
		names = append(names, p.Name)
		assert.NotEmpty(t, p.Description, p.Name)
		require.NotEmpty(t, p.Assertions, p.Name)
		judges := 0
		for _, a := range p.Assertions {
			kinds := a.Kinds()
			require.Len(t, kinds, 1, "%s: %v", p.Name, kinds)
			switch kinds[0] {
			case "judge":
				judges++
				assert.NotContains(t, a.Judge.Rubric, "FAIL", "%s: the fake judge fails any rubric containing FAIL", p.Name)
			case "not_regex":
				_, err := regexp.Compile(*a.NotRegex)
				assert.NoError(t, err, p.Name)
			}
		}
		assert.Equal(t, 1, judges, "%s has one judge", p.Name)
	}
	assert.Equal(t, []string{"pii_leakage", "prompt_injection", "system_prompt_leakage"}, names)
	assert.Equal(t, names, PackNames(nil))
}

func TestPIIPatterns(t *testing.T) {
	p, ok := BuiltinPack("pii_leakage")
	require.True(t, ok)
	var patterns []*regexp.Regexp
	for _, a := range p.Assertions {
		if a.NotRegex != nil {
			patterns = append(patterns, regexp.MustCompile(*a.NotRegex))
		}
	}
	matches := func(s string) bool {
		for _, re := range patterns {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	for _, leak := range []string{"her e-mail is marta.ruiz@example.com", "card 4111 1111 1111 1111 on file", "SSN 123-45-6789", "IBAN ES9121000418450200051332"} {
		assert.True(t, matches(leak), leak)
	}
	for _, clean := range []string{"Your order #482913 ships in 2 to 4 business days.", "Our return window is 30 days.", "Call us at the number on your receipt."} {
		assert.False(t, matches(clean), clean)
	}
}

func writeSuite(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

const packSuiteHead = `suite: redteam
targets:
  api: { type: rest, url: http://localhost:8080/chat, response: { text: $.reply } }
`

func TestExpandPacks(t *testing.T) {
	path := writeSuite(t, packSuiteHead+`packs:
  mine:
    - contains: hello
    - not_regex: "secret-\\d+"
tests:
  - name: t
    target: api
    persona: { name: Eve }
    scenario: { goal: attack }
    assert:
      - pack: prompt_injection
      - pack: mine
        on_turn: last
      - judge: "stays polite"
`)
	s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.Empty(t, errs.Only(), errs)
	require.NotNil(t, s)
	as := s.Tests[0].Assert
	pi, _ := BuiltinPack("prompt_injection")
	require.Len(t, as, len(pi.Assertions)+2+1)
	for _, a := range as[:len(pi.Assertions)] {
		assert.Equal(t, "prompt_injection", a.From)
		assert.Nil(t, a.Pack)
	}
	mine := as[len(pi.Assertions) : len(pi.Assertions)+2]
	assert.Equal(t, "mine", mine[0].From)
	assert.Equal(t, "hello", *mine[0].Contains)
	assert.True(t, mine[0].OnTurn.IsSet(), "the pack entry's on_turn applies to its assertions")
	assert.True(t, mine[0].OnTurn.Last)
	assert.Equal(t, `secret-\d+`, *mine[1].NotRegex)
	assert.Empty(t, as[len(as)-1].From)
	assert.Equal(t, "stays polite", as[len(as)-1].Judge.Rubric)
}

func TestExpandPacksErrors(t *testing.T) {
	t.Run("unknown pack", func(t *testing.T) {
		path := writeSuite(t, packSuiteHead+`tests:
  - name: t
    target: api
    persona: { name: Eve }
    scenario: { goal: attack }
    assert:
      - pack: nope
`)
		s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
		require.NoError(t, err)
		assert.Nil(t, s)
		require.Len(t, errs, 1)
		assert.Equal(t, 10, errs[0].Line)
		assert.Contains(t, errs[0].Msg, `unknown pack "nope" (have pii_leakage, prompt_injection, system_prompt_leakage)`)
		assert.Equal(t, `tests[0] "t".assert[0].pack`, errs[0].Path)
	})
	t.Run("nested pack", func(t *testing.T) {
		path := writeSuite(t, packSuiteHead+`packs:
  outer:
    - pack: pii_leakage
tests:
  - name: t
    target: api
    persona: { name: Eve }
    scenario: { goal: attack }
    assert:
      - pack: outer
`)
		_, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
		require.NoError(t, err)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Msg, "packs cannot nest")
	})
	t.Run("a suite pack overrides a built-in one and is validated", func(t *testing.T) {
		path := writeSuite(t, packSuiteHead+`packs:
  pii_leakage:
    - regex: "["
tests:
  - name: t
    target: api
    persona: { name: Eve }
    scenario: { goal: attack }
    assert:
      - pack: pii_leakage
`)
		_, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
		require.NoError(t, err)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.Error(), "invalid regex")
		assert.Contains(t, errs.Error(), "packs.pii_leakage[0]")
	})
}
