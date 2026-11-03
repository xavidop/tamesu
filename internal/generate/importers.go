package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Formats of recorded conversations ParseConversationsAs accepts.
const (
	FormatAuto      = "auto"
	FormatGeneric   = "generic"
	FormatLangSmith = "langsmith"
	FormatLangfuse  = "langfuse"
	FormatVoiceflow = "voiceflow"
)

// ImportFormats lists the formats, for flags and docs.
var ImportFormats = []string{FormatAuto, FormatGeneric, FormatLangSmith, FormatLangfuse, FormatVoiceflow}

// ParseConversationsAs reads recorded conversations in a named format:
// langsmith (a runs export: JSON or JSON Lines of runs with inputs and
// outputs; runs that share a thread are one conversation), langfuse (a
// traces export: traces with input, output and observations; traces that
// share a session are one conversation), voiceflow (a transcript's dialog
// turns, or a list of transcripts with their turns), generic (the shapes
// ParseConversations reads) or auto, which recognises the three exports by
// their keys and falls back to generic.
func ParseConversationsAs(name string, data []byte, format string) ([]Conversation, error) {
	switch format {
	case "", FormatAuto:
		values, err := decodeAll(data)
		if err != nil {
			return ParseConversations(name, data)
		}
		switch detectFormat(values) {
		case FormatLangSmith:
			return langsmith(values)
		case FormatLangfuse:
			return langfuse(values)
		case FormatVoiceflow:
			return voiceflow(values)
		}
		return ParseConversations(name, data)
	case FormatGeneric:
		return ParseConversations(name, data)
	case FormatLangSmith, FormatLangfuse, FormatVoiceflow:
		values, err := decodeAll(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", format, err)
		}
		switch format {
		case FormatLangSmith:
			return langsmith(values)
		case FormatLangfuse:
			return langfuse(values)
		default:
			return voiceflow(values)
		}
	default:
		return nil, fmt.Errorf("unknown format %q (one of %s)", format, strings.Join(ImportFormats, ", "))
	}
}

// decodeAll decodes a JSON document or JSON Lines into a flat list of the
// top-level values (an array is flattened one level).
func decodeAll(data []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
		if list, ok := v.([]any); ok {
			out = append(out, list...)
		} else {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no JSON values")
	}
	return out, nil
}

// detectFormat names the export the values look like, or "" for none.
func detectFormat(values []any) string {
	// The first value decides; the formats do not mix within one export.
	if len(values) > 0 {
		m, ok := values[0].(map[string]any)
		if !ok {
			return ""
		}
		if page, ok := m["data"].([]any); ok && !has(m, "input") && !has(m, "inputs") {
			// A page of an API: {data: [...], meta: {...}}.
			return detectFormat(page)
		}
		switch {
		case has(m, "turnID") || (has(m, "payload") && isVoiceflowTurnType(stringAt(m, "type"))):
			return FormatVoiceflow
		case isList(m["turns"]) && isVoiceflowTurns(m["turns"]), isList(m["dialog"]):
			return FormatVoiceflow
		case has(m, "observations") || has(m, "traceId") || (has(m, "input") && has(m, "output") && (has(m, "sessionId") || has(m, "latency") || has(m, "timestamp"))):
			return FormatLangfuse
		case has(m, "inputs") && (has(m, "outputs") || has(m, "run_type")):
			return FormatLangSmith
		}
		return ""
	}
	return ""
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func isList(v any) bool { _, ok := v.([]any); return ok }

func isVoiceflowTurnType(t string) bool {
	switch t {
	case "request", "text", "speak", "launch", "visual", "cardV2", "carousel", "choice", "end", "debug", "flow", "block", "path", "intent", "no-reply", "goTo", "stream":
		return true
	}
	return false
}

func isVoiceflowTurns(v any) bool {
	list, _ := v.([]any)
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			return has(m, "turnID") || (has(m, "payload") && isVoiceflowTurnType(stringAt(m, "type")))
		}
	}
	return false
}

// ---- LangSmith

// langsmith reads runs: each run's inputs hold the conversation so far
// (messages, or a single input) and its outputs the answer. Runs sharing a
// thread (metadata thread_id, session_id or conversation_id) are one
// conversation, taken from the run with the longest history; runs without
// a thread are one conversation each.
func langsmith(values []any) ([]Conversation, error) {
	type thread struct {
		conv  Conversation
		order int
	}
	threads := map[string]*thread{}
	var order []string
	n := 0
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		inputs, _ := m["inputs"].(map[string]any)
		outputs, _ := m["outputs"].(map[string]any)
		if inputs == nil && outputs == nil {
			continue
		}
		n++
		var msgs []Message
		if list, ok := anyList(inputs, "messages", "chat_history", "history"); ok {
			msgs = append(msgs, chatMessages(list)...)
		} else if q := stringAt(inputs, "input", "question", "query", "text", "message", "prompt"); q != "" {
			msgs = append(msgs, Message{Role: "user", Text: q})
		}
		if out := outputText(outputs); out != "" {
			msgs = append(msgs, Message{Role: "agent", Text: out})
		}
		if len(msgs) == 0 {
			continue
		}
		key := threadKey(m, inputs)
		if key == "" {
			key = fmt.Sprintf("run %d", n)
			if id := stringAt(m, "id"); id != "" {
				key = id
			}
		}
		t, ok := threads[key]
		if !ok {
			t = &thread{conv: Conversation{ID: nameOf(m, key)}, order: n}
			threads[key] = t
			order = append(order, key)
		}
		if len(msgs) > len(t.conv.Messages) {
			t.conv.Messages = msgs
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("langsmith: no runs with inputs and outputs")
	}
	out := make([]Conversation, 0, len(order))
	for _, k := range order {
		out = append(out, threads[k].conv)
	}
	return out, nil
}

// threadKey finds the id that groups a run's turns into one conversation.
func threadKey(run, inputs map[string]any) string {
	for _, meta := range []map[string]any{mapAt(mapAt(run, "extra"), "metadata"), mapAt(run, "metadata"), inputs, run} {
		if meta == nil {
			continue
		}
		if k := stringAt(meta, "thread_id", "session_id", "conversation_id", "ls_thread_id"); k != "" {
			return k
		}
	}
	return ""
}

func nameOf(m map[string]any, fallback string) string {
	if n := stringAt(m, "name"); n != "" && n != "ChatOpenAI" && n != "RunnableSequence" {
		return n + " " + shortID(fallback)
	}
	return fallback
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// outputText is the answer in a run's or trace's outputs, whatever shape.
func outputText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s := stringAt(x, "output", "answer", "text", "content", "result", "response", "message"); s != "" {
			return s
		}
		if list, ok := anyList(x, "messages"); ok {
			msgs := chatMessages(list)
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "agent" {
					return msgs[i].Text
				}
			}
		}
		if gens, ok := x["generations"].([]any); ok && len(gens) > 0 {
			inner := gens[0]
			if l, ok := inner.([]any); ok && len(l) > 0 {
				inner = l[0]
			}
			if g, ok := inner.(map[string]any); ok {
				if s := stringAt(g, "text"); s != "" {
					return s
				}
				return outputText(g["message"])
			}
		}
		for _, k := range []string{"output", "message", "content", "data"} {
			if sub, ok := x[k]; ok {
				if s := outputText(sub); s != "" {
					return s
				}
			}
		}
		if kwargs := mapAt(x, "kwargs"); kwargs != nil {
			return outputText(kwargs)
		}
	case []any:
		var parts []string
		for _, it := range x {
			if s := outputText(it); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// chatMessages reads a list of chat messages in the shapes of the OpenAI,
// LangChain and Anthropic SDKs: role or type, content as a string or parts.
func chatMessages(list []any) []Message {
	var out []Message
	for _, it := range list {
		if nested, ok := it.([]any); ok {
			// LangChain batches: a list of message lists.
			out = append(out, chatMessages(nested)...)
			continue
		}
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if kwargs := mapAt(m, "kwargs"); kwargs != nil && !has(m, "role") {
			// LangChain serialised: {id: [..., "HumanMessage"], kwargs: {content}}
			role := ""
			if ids, ok := m["id"].([]any); ok && len(ids) > 0 {
				role, _ = ids[len(ids)-1].(string)
			}
			if role == "" {
				role = stringAt(kwargs, "type", "role")
			}
			if r := langchainRole(role); r != "" {
				if text := contentText(kwargs["content"]); text != "" {
					out = append(out, Message{Role: r, Text: text})
				}
			}
			continue
		}
		role := langchainRole(stringAt(m, "role", "type", "speaker", "author"))
		if role == "" {
			continue
		}
		text := contentText(m["content"])
		if text == "" {
			text = stringAt(m, "text", "message")
		}
		if text == "" {
			continue
		}
		out = append(out, Message{Role: role, Text: text})
	}
	return out
}

// langchainRole maps the message types of LangChain and the chat SDKs.
func langchainRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "human", "humanmessage", "humanmessagechunk":
		return "user"
	case "ai", "aimessage", "aimessagechunk", "assistant":
		return "agent"
	}
	return NormalizeRole(role)
}

// contentText flattens a message content: a string, or a list of parts
// with text.
func contentText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			switch pp := p.(type) {
			case string:
				parts = append(parts, pp)
			case map[string]any:
				if t := stringAt(pp, "text"); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		return stringAt(x, "text", "content")
	}
	return ""
}

func mapAt(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	sub, _ := m[k].(map[string]any)
	return sub
}

func anyList(m map[string]any, keys ...string) ([]any, bool) {
	for _, k := range keys {
		if l, ok := m[k].([]any); ok {
			return l, true
		}
	}
	return nil, false
}

// ---- Langfuse

// langfuse reads traces: a trace's input is the conversation so far (a
// messages list, or a prompt) and its output the answer; the GENERATION
// observations serve when the trace carries none. Traces sharing a
// sessionId are one conversation, from the trace with the longest history.
func langfuse(values []any) ([]Conversation, error) {
	type sess struct{ conv Conversation }
	sessions := map[string]*sess{}
	var order []string
	n := 0
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if list, ok := m["data"].([]any); ok && !has(m, "input") {
			// A paginated API page: {data: [traces], meta: {...}}.
			sub, err := langfuse(list)
			if err == nil {
				return sub, nil
			}
			continue
		}
		input, output := m["input"], m["output"]
		if input == nil && output == nil {
			if obs, ok := m["observations"].([]any); ok {
				input, output = generationIO(obs)
			}
		}
		msgs := traceMessages(input, output)
		if len(msgs) == 0 {
			continue
		}
		n++
		key := stringAt(m, "sessionId", "session_id")
		if key == "" {
			key = stringAt(m, "id", "traceId")
		}
		if key == "" {
			key = fmt.Sprintf("trace %d", n)
		}
		s, ok := sessions[key]
		if !ok {
			s = &sess{conv: Conversation{ID: nameOf(m, key)}}
			sessions[key] = s
			order = append(order, key)
		}
		if len(msgs) > len(s.conv.Messages) {
			s.conv.Messages = msgs
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("langfuse: no traces with input and output")
	}
	out := make([]Conversation, 0, len(order))
	for _, k := range order {
		out = append(out, sessions[k].conv)
	}
	return out, nil
}

// generationIO is the input and output of the last GENERATION observation.
func generationIO(obs []any) (any, any) {
	var in, out any
	for _, o := range obs {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		if t := stringAt(m, "type"); t != "" && t != "GENERATION" {
			continue
		}
		if m["input"] != nil || m["output"] != nil {
			in, out = m["input"], m["output"]
		}
	}
	return in, out
}

// traceMessages builds a conversation from a trace's input and output.
func traceMessages(input, output any) []Message {
	var msgs []Message
	switch x := input.(type) {
	case string:
		msgs = append(msgs, Message{Role: "user", Text: x})
	case []any:
		msgs = append(msgs, chatMessages(x)...)
	case map[string]any:
		if list, ok := anyList(x, "messages", "chat_history", "history"); ok {
			msgs = append(msgs, chatMessages(list)...)
		} else if q := stringAt(x, "input", "question", "query", "text", "message", "prompt", "content"); q != "" {
			msgs = append(msgs, Message{Role: "user", Text: q})
		}
	}
	if out := outputText(output); out != "" {
		msgs = append(msgs, Message{Role: "agent", Text: out})
	}
	return msgs
}

// ---- Voiceflow

// voiceflow reads a transcript's dialog turns (type request for the user,
// text or speak for the agent, each with a payload), one transcript per
// file, or a list of transcripts that each carry turns.
func voiceflow(values []any) ([]Conversation, error) {
	var out []Conversation
	var loose []any
	n := 0
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if turns, ok := anyList(m, "turns", "dialog", "transcript"); ok {
			n++
			id := stringAt(m, "transcriptID", "_id", "id", "sessionID", "name")
			if id == "" {
				id = fmt.Sprintf("transcript %d", n)
			}
			out = append(out, Conversation{ID: id, Messages: voiceflowTurns(turns)})
			continue
		}
		loose = append(loose, m)
	}
	if len(loose) > 0 {
		id := "transcript"
		if n > 0 {
			id = fmt.Sprintf("transcript %d", n+1)
		}
		out = append(out, Conversation{ID: id, Messages: voiceflowTurns(loose)})
	}
	kept := out[:0]
	for _, c := range out {
		if len(c.Messages) > 0 {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("voiceflow: no request or text turns")
	}
	return kept, nil
}

// voiceflowTurns maps dialog turns to messages: a request is what the user
// said (payload.payload.query, or the intent's label), text and speak are
// what the agent answered (payload.message or payload.payload.message).
func voiceflowTurns(turns []any) []Message {
	var out []Message
	for _, t := range turns {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		payload := mapAt(m, "payload")
		inner := mapAt(payload, "payload")
		switch stringAt(m, "type") {
		case "request", "intent":
			text := stringAt(inner, "query", "label", "text", "message", "utterance")
			if text == "" {
				text = stringAt(payload, "query", "label", "text", "message", "utterance")
			}
			if text == "" && stringAt(payload, "type") == "launch" {
				continue
			}
			if text != "" {
				out = append(out, Message{Role: "user", Text: text})
			}
		case "text", "speak":
			text := stringAt(payload, "message", "text")
			if text == "" {
				text = stringAt(inner, "message", "text")
			}
			if text == "" {
				if slate, ok := inner["slate"].(map[string]any); ok {
					text = slateText(slate)
				}
			}
			if text != "" {
				out = append(out, Message{Role: "agent", Text: text})
			}
		}
	}
	return out
}

// slateText flattens a Slate rich-text document to its text.
func slateText(slate map[string]any) string {
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["text"].(string); ok {
				b.WriteString(t)
			}
			if children, ok := x["children"].([]any); ok {
				for _, c := range children {
					walk(c)
				}
				if _, isPara := x["type"]; isPara || x["text"] == nil {
					b.WriteString("\n")
				}
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(slate["content"])
	return strings.TrimSpace(b.String())
}
