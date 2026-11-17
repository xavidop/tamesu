# desktop-editor: the desktop example

`suite.yaml` tests a text editor the way a person would, through the operating system's
accessibility tree: a `goal` step has the agent type a note that ends in
`{{secrets.NOTE_PIN}}` (the agent types the placeholder; the value is substituted when it is
typed), an `expect` step checks the window agentically, and deterministic actions open the Edit
menu, pick an item of it, press a key and pin the text.

`DESKTOP_APP` names the editor. The suite defaults to TextEdit, which is on every Mac; on Linux
`mousepad` works as it is. The menu steps name the Edit menu and its Select All item by name
alone, which holds on both; an editor that calls them something else needs those two steps
changed. TextEdit may open on its file picker instead of a document: the agent sees the picker
and clicks its New Document button on its way to the goal.

```sh
export NOTE_PIN=4821
tamesu run --config examples/desktop-editor/tamesu.yaml                        # macOS, with a real model (GEMINI_API_KEY)
DESKTOP_APP=mousepad tamesu run --config examples/desktop-editor/tamesu.yaml   # Linux
```

tamesu downloads the pinned agent-desktop on first use. `tamesu doctor --config
examples/desktop-editor/tamesu.yaml` says whether the desktop can be read: on macOS the
application that runs tamesu needs the Accessibility permission.

Without keys, the fake agent replays `llmfake-script.json` against the real editor (a tamesu
binary built with `-tags llmfake`):

```sh
DESKTOP_APP=mousepad NOTE_PIN=4821 TAMESU_LLMFAKE_SCRIPT=examples/desktop-editor/llmfake-script.json \
  tamesu run --config examples/desktop-editor/tamesu.llmfake.yaml
```

`make integration-desktop` runs exactly that against `mousepad` in a headless Linux session (in
a container on other systems), after the target's own integration test.

The run directory has `steps.json`, a screenshot per step where the system allows them, and
`agent-desktop.jsonl`, the trace of what agent-desktop read and did. The editor is closed at the
end of the run; because it holds unsaved text it asks to save, and tamesu terminates it.
