package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const langsmithRuns = `{"id":"r1","name":"support","run_type":"chain","inputs":{"messages":[{"role":"user","content":"Can I return shoes bought 40 days ago?"}]},"outputs":{"output":"Our window is 30 days, so no."},"extra":{"metadata":{"thread_id":"t-1"}}}
{"id":"r2","name":"support","run_type":"chain","inputs":{"messages":[{"role":"user","content":"Can I return shoes bought 40 days ago?"},{"role":"assistant","content":"Our window is 30 days, so no."},{"role":"user","content":"Any voucher then?"}]},"outputs":{"messages":[{"role":"assistant","content":"Yes, a 10% voucher."}]},"extra":{"metadata":{"thread_id":"t-1"}}}
{"id":"r3","name":"ChatOpenAI","run_type":"llm","inputs":{"messages":[[{"id":["langchain","schema","messages","HumanMessage"],"kwargs":{"content":"How long is shipping?"}}]]},"outputs":{"generations":[[{"text":"Three to five days."}]]}}
`

func TestLangSmithImport(t *testing.T) {
	convs, err := ParseConversationsAs("runs.jsonl", []byte(langsmithRuns), FormatAuto)
	require.NoError(t, err)
	require.Len(t, convs, 2, "two runs of one thread are one conversation; the llm run another")
	assert.Equal(t, "support t-1", convs[0].ID)
	assert.Equal(t, []Message{
		{Role: "user", Text: "Can I return shoes bought 40 days ago?"},
		{Role: "agent", Text: "Our window is 30 days, so no."},
		{Role: "user", Text: "Any voucher then?"},
		{Role: "agent", Text: "Yes, a 10% voucher."},
	}, convs[0].Messages, "the longest history of the thread, plus its answer")
	assert.Equal(t, []Message{{Role: "user", Text: "How long is shipping?"}, {Role: "agent", Text: "Three to five days."}}, convs[1].Messages, "LangChain serialised messages and generations")

	_, err = ParseConversationsAs("runs.jsonl", []byte(`{"id":"x"}`), FormatLangSmith)
	assert.ErrorContains(t, err, "no runs")
	_, err = ParseConversationsAs("runs.jsonl", []byte(langsmithRuns), "slack")
	assert.ErrorContains(t, err, "unknown format")
}

const langfuseTraces = `{"data":[
 {"id":"tr1","name":"chat","sessionId":"s1","timestamp":"2026-11-01T10:00:00Z","input":{"messages":[{"role":"system","content":"You help."},{"role":"user","content":"Hola, quiero devolver unos zapatos"}]},"output":{"role":"assistant","content":"Claro, ¿cuándo los compraste?"}},
 {"id":"tr2","name":"chat","sessionId":"s1","timestamp":"2026-11-01T10:01:00Z","input":{"messages":[{"role":"user","content":"Hola, quiero devolver unos zapatos"},{"role":"assistant","content":"Claro, ¿cuándo los compraste?"},{"role":"user","content":"Hace 40 días"}]},"output":"Lo siento, el plazo es de 30 días."},
 {"id":"tr3","name":"faq","timestamp":"2026-11-01T11:00:00Z","observations":[{"type":"SPAN","input":"x"},{"type":"GENERATION","input":[{"role":"user","content":"Shipping time?"}],"output":{"content":"3 to 5 days."}}]}
], "meta": {"page": 1}}`

func TestLangfuseImport(t *testing.T) {
	convs, err := ParseConversationsAs("traces.json", []byte(langfuseTraces), FormatAuto)
	require.NoError(t, err)
	require.Len(t, convs, 2)
	assert.Equal(t, "chat s1", convs[0].ID)
	assert.Equal(t, []Message{
		{Role: "user", Text: "Hola, quiero devolver unos zapatos"},
		{Role: "agent", Text: "Claro, ¿cuándo los compraste?"},
		{Role: "user", Text: "Hace 40 días"},
		{Role: "agent", Text: "Lo siento, el plazo es de 30 días."},
	}, convs[0].Messages, "the session's longest trace; the system message is dropped")
	assert.Equal(t, []Message{{Role: "user", Text: "Shipping time?"}, {Role: "agent", Text: "3 to 5 days."}}, convs[1].Messages, "a trace without input falls back to its generation")
}

const voiceflowTranscript = `[
 {"turnID":"1","type":"launch","payload":{"type":"launch"}},
 {"turnID":"2","type":"text","payload":{"message":"Welcome to Mochi Shoes, how can I help?"}},
 {"turnID":"3","type":"request","payload":{"type":"text","payload":{"query":"I want to return shoes I bought 40 days ago"}}},
 {"turnID":"4","type":"text","payload":{"payload":{"slate":{"content":[{"children":[{"text":"Our return window is "},{"text":"30 days","fontWeight":"700"}]}]}}}},
 {"turnID":"5","type":"request","payload":{"type":"intent","payload":{"query":"ok thanks","intent":{"name":"thanks"}}}},
 {"turnID":"6","type":"speak","payload":{"message":"You're welcome!"}},
 {"turnID":"7","type":"end","payload":{}}
]`

func TestVoiceflowImport(t *testing.T) {
	convs, err := ParseConversationsAs("transcript.json", []byte(voiceflowTranscript), FormatAuto)
	require.NoError(t, err)
	require.Len(t, convs, 1)
	assert.Equal(t, []Message{
		{Role: "agent", Text: "Welcome to Mochi Shoes, how can I help?"},
		{Role: "user", Text: "I want to return shoes I bought 40 days ago"},
		{Role: "agent", Text: "Our return window is 30 days"},
		{Role: "user", Text: "ok thanks"},
		{Role: "agent", Text: "You're welcome!"},
	}, convs[0].Messages, "request turns are the user, text and speak the agent, Slate is flattened")

	many := `[{"_id":"a","turns":` + voiceflowTranscript + `},{"_id":"b","turns":[{"type":"request","payload":{"payload":{"query":"hi"}}},{"type":"text","payload":{"message":"hello"}}]}]`
	convs, err = ParseConversationsAs("transcripts.json", []byte(many), FormatVoiceflow)
	require.NoError(t, err)
	require.Len(t, convs, 2)
	assert.Equal(t, "a", convs[0].ID)
	assert.Equal(t, "b", convs[1].ID)
	assert.Len(t, convs[1].Messages, 2)
}

func TestGenericStaysGeneric(t *testing.T) {
	generic := `[{"id":"c1","turns":[{"role":"user","text":"hi"},{"role":"agent","text":"hello"}]}]`
	convs, err := ParseConversationsAs("chats.json", []byte(generic), FormatAuto)
	require.NoError(t, err)
	require.Len(t, convs, 1)
	assert.Equal(t, "c1", convs[0].ID)
	csv := "conversation,role,text\nc1,user,hi\nc1,agent,hello\n"
	convs, err = ParseConversationsAs("chats.csv", []byte(csv), FormatAuto)
	require.NoError(t, err)
	assert.Len(t, convs, 1)
}
