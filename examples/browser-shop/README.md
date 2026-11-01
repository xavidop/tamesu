# browser-shop: the browser example

[`examples/webapp`](../webapp/) is Mochi Shoes, a tiny web shop (login, product search, cart)
with no dependencies beyond Go's standard library. `suite.yaml` tests it the way a person would:
a `navigate` action opens the login page, a `goal` step has the browser agent log in as
`{{vars.user}}` with `{{secrets.TEST_PASSWORD}}` (the agent types the placeholder; the browser
substitutes the value), an `expect` step checks the page agentically, another goal searches for
a product and adds it to the cart, and deterministic actions pin the cart URL and text.

```sh
go run ./examples/webapp &                               # http://127.0.0.1:18090
export TEST_PASSWORD=correct-horse
tamesu run --config examples/browser-shop/tamesu.yaml    # with a real model (GEMINI_API_KEY)
```

Without keys, the fake browser agent replays `llmfake-script.json` against the real Chromium
(a tamesu binary built with `-tags llmfake`):

```sh
TAMESU_LLMFAKE_SCRIPT=examples/browser-shop/llmfake-script.json TEST_PASSWORD=correct-horse \
  tamesu run --config examples/browser-shop/tamesu.llmfake.yaml
```

The run directory has a screenshot per step (`screenshots/step-NN.png`), `steps.json`,
`trace.har` and `console.log`. `make integration` runs this flow headless; GitHub's runners
ship Chrome, elsewhere tamesu finds Chrome, Chromium or Edge, or downloads Chrome for Testing
into `~/.cache/tamesu`.
