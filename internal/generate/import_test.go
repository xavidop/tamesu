package generate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/xavidop/tamesu/internal/config"
)

func TestParseConversations(t *testing.T) {
	tests := []struct {
		name string
		file string
		data string
		want []Conversation
	}{
		{
			name: "json conversations with turns",
			file: "export.json",
			data: `[{"id":"c1","turns":[{"role":"user","text":"hi"},{"role":"assistant","content":"hello"}]},
			        {"name":"c2","messages":[{"speaker":"customer","message":"bye"}]}]`,
			want: []Conversation{{ID: "c1", Messages: []Message{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "hello"}}}, {ID: "c2", Messages: []Message{{Role: "customer", Text: "bye"}}}},
		},
		{
			name: "json flat messages grouped by conversation id",
			file: "flat.json",
			data: `[{"conversation_id":7,"role":"user","text":"a"},{"conversation_id":"8","role":"user","text":"b"},{"conversation_id":7,"role":"agent","text":"c"}]`,
			want: []Conversation{{ID: "7", Messages: []Message{{Role: "user", Text: "a"}, {Role: "agent", Text: "c"}}}, {ID: "8", Messages: []Message{{Role: "user", Text: "b"}}}},
		},
		{
			name: "json wrapper object and a tamesu transcript",
			file: "wrap.json",
			data: `{"conversations":[{"turns":[{"role":"user","text":"x"}]}]}`,
			want: []Conversation{{ID: "conversation 1", Messages: []Message{{Role: "user", Text: "x"}}}},
		},
		{
			name: "jsonl of conversations",
			file: "a.jsonl",
			data: "{\"id\":\"c1\",\"turns\":[{\"role\":\"user\",\"text\":\"hi\"}]}\n{\"id\":\"c2\",\"turns\":[{\"role\":\"user\",\"text\":\"yo\"}]}\n",
			want: []Conversation{{ID: "c1", Messages: []Message{{Role: "user", Text: "hi"}}}, {ID: "c2", Messages: []Message{{Role: "user", Text: "yo"}}}},
		},
		{
			name: "csv",
			file: "a.csv",
			data: "conversation_id,role,text\n1,user,\"Hello, there\"\n1,agent,Hi\n2,user,Bye\n",
			want: []Conversation{{ID: "1", Messages: []Message{{Role: "user", Text: "Hello, there"}, {Role: "agent", Text: "Hi"}}}, {ID: "2", Messages: []Message{{Role: "user", Text: "Bye"}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConversations(tt.file, []byte(tt.data))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := ParseConversations("a.csv", []byte("id,words\n1,x\n"))
	assert.ErrorContains(t, err, "role column")
	_, err = ParseConversations("a.json", []byte(`[1, 2]`))
	assert.ErrorContains(t, err, "want conversations or messages")
	_, err = ParseConversations("a.txt", nil)
	assert.ErrorContains(t, err, "unknown format")
}

func TestImport(t *testing.T) {
	convs := []Conversation{
		{ID: "refund", Messages: []Message{
			{Role: "assistant", Text: "Welcome!"}, // an opening greeting is not replayable
			{Role: "user", Text: "Where is my refund?"},
			{Role: "user", Text: "Order 1234"}, // merged with the line above
			{Role: "system", Text: "ignored"},
			{Role: "assistant", Text: "It was sent yesterday."},
			{Role: "user", Text: "Thanks"},
			{Role: "assistant", Text: "Any time."},
		}},
		{ID: "greeting only", Messages: []Message{{Role: "assistant", Text: "Hello"}}},
		{ID: "refund", Messages: []Message{{Role: "user", Text: "again"}, {Role: "agent", Text: "ok"}}},
	}
	res, err := Import(convs, ImportOptions{SourceName: "exports/chats.json", Target: "api", Tags: []string{"imported"}})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Tests)
	assert.Equal(t, []string{"greeting only"}, res.Skipped)
	assert.Empty(t, res.Golden, "inline turns without a golden dir")
	var s config.Suite
	require.NoError(t, yaml.Unmarshal(res.Suite, &s), string(res.Suite))
	assert.Equal(t, "chats", s.Suite)
	require.Len(t, s.Tests, 2)
	first := s.Tests[0]
	assert.Equal(t, "refund", first.Name)
	assert.Equal(t, []string{"imported"}, first.Tags)
	require.NotNil(t, first.Scenario)
	assert.Equal(t, "", first.Scenario.Goal)
	assert.Equal(t, []config.ScriptStep{{Say: "Where is my refund?\nOrder 1234"}, {Say: "Thanks"}}, first.Scenario.Script)
	require.Len(t, first.Assert, 1)
	require.NotNil(t, first.Assert[0].Golden)
	assert.Equal(t, []string{"It was sent yesterday.", "Any time."}, first.Assert[0].Golden.Turns)
	assert.Equal(t, "similar", first.Assert[0].Golden.Mode)
	assert.Equal(t, "refund (2)", s.Tests[1].Name)
	assert.Equal(t, config.TargetREST, s.Targets["api"].Type)
	assert.True(t, strings.HasPrefix(string(res.Suite), "# Imported by tamesu import from chats.json: 2 conversation(s)."))

	errs := config.Validate(&s, config.DefaultProject(), nil)
	assert.False(t, errs.HasErrors(), "%s", errs)

	files, err := Import(convs, ImportOptions{SourceName: "chats.csv", Target: "bot", Type: config.TargetGRPC, GoldenDir: "golden", GoldenMode: "exact"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"golden/refund.json": {"It was sent yesterday.", "Any time."}, "golden/refund-2.json": {"ok"}}, files.Golden)
	require.NoError(t, yaml.Unmarshal(files.Suite, &s))
	assert.Equal(t, "golden/refund.json", s.Tests[0].Assert[0].Golden.File)
	assert.Equal(t, "exact", s.Tests[0].Assert[0].Golden.Mode)

	_, err = Import(convs, ImportOptions{Target: "web", Type: config.TargetBrowser})
	assert.ErrorContains(t, err, "not browser")
	_, err = Import(convs[1:2], ImportOptions{Target: "api"})
	assert.ErrorContains(t, err, "no conversation has both")
	_, err = Import(convs, ImportOptions{Target: "api", GoldenMode: "fuzzy"})
	assert.ErrorContains(t, err, "unknown golden mode")
}
