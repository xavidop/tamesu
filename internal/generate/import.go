package generate

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xavidop/tamesu/internal/config"
)

// Message is one line of a recorded conversation.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Conversation is a recorded exchange between a user and an agent.
type Conversation struct {
	ID       string
	Messages []Message
}

// ParseConversations reads recorded conversations from a file: a JSON array
// (conversations with turns, or flat messages with a conversation id), a
// JSON object with conversations or turns, JSON Lines of either, or a CSV
// with a header naming the conversation, role and text columns. The
// format follows the file's extension (.json, .jsonl, .ndjson, .csv).
func ParseConversations(name string, data []byte) ([]Conversation, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".csv":
		return parseCSV(data)
	case ".jsonl", ".ndjson":
		return parseJSONL(data)
	case ".json", "":
		return parseJSON(data)
	default:
		return nil, fmt.Errorf("%s: unknown format (want .json, .jsonl, .ndjson or .csv)", name)
	}
}

var (
	idKeys   = []string{"conversation_id", "conversation", "session_id", "session", "id", "name"}
	roleKeys = []string{"role", "speaker", "from", "author"}
	textKeys = []string{"text", "content", "message", "utterance", "transcript"}
	listKeys = []string{"turns", "messages", "transcript", "history"}
)

func stringAt(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64:
				return strings.TrimSuffix(fmt.Sprintf("%.0f", x), ".0")
			case json.Number:
				return x.String()
			}
		}
	}
	return ""
}

func listAt(m map[string]any) ([]any, bool) {
	for _, k := range listKeys {
		if v, ok := m[k].([]any); ok {
			return v, true
		}
	}
	return nil, false
}

func messageOf(m map[string]any) (Message, bool) {
	role := stringAt(m, roleKeys...)
	text := stringAt(m, textKeys...)
	if role == "" || text == "" {
		return Message{}, false
	}
	return Message{Role: role, Text: text}, true
}

// collector groups messages by conversation id in order of first appearance.
type collector struct {
	order []string
	byID  map[string]*Conversation
	n     int
}

func (c *collector) add(id string, m Message) {
	if c.byID == nil {
		c.byID = map[string]*Conversation{}
	}
	if id == "" {
		id = "conversation 1"
	}
	conv, ok := c.byID[id]
	if !ok {
		conv = &Conversation{ID: id}
		c.byID[id] = conv
		c.order = append(c.order, id)
	}
	conv.Messages = append(conv.Messages, m)
}

func (c *collector) conversation(m map[string]any, turns []any) {
	c.n++
	id := stringAt(m, idKeys...)
	if id == "" {
		id = fmt.Sprintf("conversation %d", c.n)
	}
	conv := &Conversation{ID: id}
	for _, t := range turns {
		if tm, ok := t.(map[string]any); ok {
			if msg, ok := messageOf(tm); ok {
				conv.Messages = append(conv.Messages, msg)
			}
		}
	}
	if c.byID == nil {
		c.byID = map[string]*Conversation{}
	}
	if _, dup := c.byID[id]; dup {
		id = fmt.Sprintf("%s (%d)", id, c.n)
		conv.ID = id
	}
	c.byID[id] = conv
	c.order = append(c.order, id)
}

// object takes one decoded JSON value: a conversation, a message, or a
// wrapper around a list of either.
func (c *collector) object(v any) error {
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			if err := c.object(it); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if turns, ok := listAt(x); ok {
			if list, ok := x["conversations"].([]any); ok && len(turns) == 0 {
				return c.object(list)
			}
			c.conversation(x, turns)
			return nil
		}
		if list, ok := x["conversations"].([]any); ok {
			return c.object(list)
		}
		if msg, ok := messageOf(x); ok {
			c.add(stringAt(x, idKeys...), msg)
			return nil
		}
		return errors.New("an object is a conversation (turns or messages) or a message (role and text)")
	default:
		return fmt.Errorf("unexpected %T; want conversations or messages", v)
	}
}

func (c *collector) result() []Conversation {
	out := make([]Conversation, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, *c.byID[id])
	}
	return out
}

func parseJSON(data []byte) ([]Conversation, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	var c collector
	if err := c.object(v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return c.result(), nil
}

func parseJSONL(data []byte) ([]Conversation, error) {
	var c collector
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for line := 1; ; line++ {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("jsonl line %d: %w", line, err)
		}
		if err := c.object(v); err != nil {
			return nil, fmt.Errorf("jsonl line %d: %w", line, err)
		}
	}
	return c.result(), nil
}

func parseCSV(data []byte) ([]Conversation, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	find := func(keys ...string) int {
		for _, k := range keys {
			if i, ok := col[k]; ok {
				return i
			}
		}
		return -1
	}
	idCol, roleCol, textCol := find(idKeys...), find(roleKeys...), find(textKeys...)
	if roleCol < 0 || textCol < 0 {
		return nil, fmt.Errorf("csv: the header needs a role column and a text column (have %s)", strings.Join(header, ", "))
	}
	var c collector
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv line %d: %w", line, err)
		}
		if roleCol >= len(rec) || textCol >= len(rec) {
			continue
		}
		id := ""
		if idCol >= 0 && idCol < len(rec) {
			id = rec[idCol]
		}
		c.add(id, Message{Role: rec[roleCol], Text: rec[textCol]})
	}
	return c.result(), nil
}

// ImportOptions shape the suite written from conversations.
type ImportOptions struct {
	// Suite is the suite name (default: from the source file name).
	Suite      string
	SourceName string
	Target     string
	// Type is the target's type (default rest).
	Type string
	// GoldenDir, when set, puts each conversation's agent turns in
	// <GoldenDir>/<test>.json next to the suite instead of inline turns.
	GoldenDir string
	// GoldenMode is exact, similar (the default) or judge.
	GoldenMode string
	Tags       []string
}

// ImportResult is the suite and its reference files.
type ImportResult struct {
	Suite []byte
	// Golden maps a path relative to the suite to the agent turns to write
	// there (only with ImportOptions.GoldenDir).
	Golden map[string][]string
	Tests  int
	// Skipped names the conversations that gave no test (no user turn).
	Skipped []string
}

// NormalizeRole maps the many names of the two sides to user or agent; ""
// for roles that are neither (system, tool).
func NormalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "human", "customer", "caller", "client", "visitor", "member":
		return "user"
	case "agent", "assistant", "bot", "ai", "model", "system_agent", "operator":
		return "agent"
	}
	return ""
}

// clean keeps the user and agent lines, merges consecutive lines of one
// side and drops an opening agent greeting the user never prompted.
func clean(c Conversation) Conversation {
	out := Conversation{ID: c.ID}
	for _, m := range c.Messages {
		role := NormalizeRole(m.Role)
		text := strings.TrimSpace(m.Text)
		if role == "" || text == "" {
			continue
		}
		if len(out.Messages) == 0 && role == "agent" {
			continue
		}
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Text += "\n" + text
			continue
		}
		out.Messages = append(out.Messages, Message{Role: role, Text: text})
	}
	return out
}

// Import turns conversations into a suite: one test per conversation whose
// script says the user's lines in order and whose golden assertion holds
// the agent's answers.
func Import(convs []Conversation, o ImportOptions) (*ImportResult, error) {
	if o.Target == "" {
		return nil, fmt.Errorf("import: a target name is required")
	}
	typ := o.Type
	if typ == "" {
		typ = config.TargetREST
	}
	if !contains(config.TargetTypes(), typ) {
		return nil, fmt.Errorf("import: unknown target type %q (one of %s)", typ, strings.Join(config.TargetTypes(), ", "))
	}
	if typ == config.TargetBrowser {
		return nil, fmt.Errorf("import: conversations replay on conversational targets, not browser")
	}
	mode := o.GoldenMode
	if mode == "" {
		mode = "similar"
	}
	if !contains(config.GoldenModes, mode) {
		return nil, fmt.Errorf("import: unknown golden mode %q (one of %s)", mode, strings.Join(config.GoldenModes, ", "))
	}
	name := o.Suite
	if name == "" {
		name = slug(strings.TrimSuffix(filepath.Base(o.SourceName), filepath.Ext(o.SourceName)))
	}
	if name == "" {
		name = "imported"
	}
	res := &ImportResult{Golden: map[string][]string{}}
	tests := &yaml.Node{Kind: yaml.SequenceNode}
	seen := map[string]int{}
	for _, raw := range convs {
		c := clean(raw)
		var says, answers []string
		for _, m := range c.Messages {
			if m.Role == "user" {
				says = append(says, m.Text)
			} else {
				answers = append(answers, m.Text)
			}
		}
		if len(says) == 0 || len(answers) == 0 {
			res.Skipped = append(res.Skipped, c.ID)
			continue
		}
		tname := strings.TrimSpace(c.ID)
		seen[tname]++
		if n := seen[tname]; n > 1 {
			tname = fmt.Sprintf("%s (%d)", tname, n)
		}
		n := mapping(key("name"), scalar(tname), key("target"), scalar(o.Target))
		if tags := cleanTags(o.Tags); len(tags) > 0 {
			n.Content = append(n.Content, key("tags"), flowSeq(tags))
		}
		script := &yaml.Node{Kind: yaml.SequenceNode}
		for _, s := range says {
			script.Content = append(script.Content, mapping(key("say"), scalar(s)))
		}
		n.Content = append(n.Content, key("scenario"), mapping(key("script"), script))
		golden := mapping()
		if o.GoldenDir != "" {
			rel := filepath.ToSlash(filepath.Join(o.GoldenDir, slug(tname)+".json"))
			for i := 2; res.Golden[rel] != nil; i++ {
				rel = filepath.ToSlash(filepath.Join(o.GoldenDir, fmt.Sprintf("%s-%d.json", slug(tname), i)))
			}
			res.Golden[rel] = answers
			golden.Content = append(golden.Content, key("file"), scalar(rel))
		} else {
			turns := &yaml.Node{Kind: yaml.SequenceNode}
			for _, a := range answers {
				turns.Content = append(turns.Content, scalar(a))
			}
			golden.Content = append(golden.Content, key("turns"), turns)
		}
		golden.Content = append(golden.Content, key("mode"), scalar(mode))
		asserts := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{mapping(key("golden"), golden)}}
		n.Content = append(n.Content, key("assert"), asserts)
		tests.Content = append(tests.Content, n)
		res.Tests++
	}
	if res.Tests == 0 {
		return nil, fmt.Errorf("import: no conversation has both user and agent lines")
	}
	root := mapping(key("suite"), scalar(name))
	root.Content = append(root.Content, key("targets"), mapping(key(o.Target), targetStub(typ)))
	root.Content = append(root.Content, key("tests"), tests)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Imported by tamesu import from %s: %d conversation(s).\n", filepath.Base(o.SourceName), res.Tests)
	buf.WriteString("# Each test says the user's lines in order and compares the agent's answers with the recorded ones.\n")
	buf.WriteString("# The target is a stub: point url (and the request/response mapping) at your agent.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	res.Suite = buf.Bytes()
	return res, nil
}
