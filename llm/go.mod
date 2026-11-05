module github.com/xavidop/tamesu/llm

go 1.27

require (
	github.com/firebase/genkit/go v1.14.0
	github.com/mark3labs/mcp-go v1.5.0
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0
	github.com/xavidop/tamesu/audio v0.0.0
	github.com/xavidop/tamesu/llm/plugins/assemblyai v0.0.0
	github.com/xavidop/tamesu/llm/plugins/azure v0.0.0
	github.com/xavidop/tamesu/llm/plugins/cartesia v0.0.0
	github.com/xavidop/tamesu/llm/plugins/deepgram v0.0.0
	github.com/xavidop/tamesu/llm/plugins/elevenlabs v0.0.0
	github.com/xavidop/tamesu/llm/plugins/gladia v0.0.0
	github.com/xavidop/tamesu/llm/plugins/googlespeech v0.0.0
	github.com/xavidop/tamesu/llm/plugins/openaispeech v0.0.0
	github.com/xavidop/tamesu/llm/plugins/rime v0.0.0
	github.com/xavidop/tamesu/llm/plugins/soniox v0.0.0
	github.com/xavidop/tamesu/llm/plugins/speechmatics v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.1 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	github.com/anthropics/anthropic-sdk-go v1.23.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/blues/jsonata-go v1.5.4 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.4 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.5 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/dotprompt/go v0.0.0-20260708220100-73beb993ac95 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.23 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mbleigh/raymond v0.0.0-20250414171441-6b3a58ab9e0a // indirect
	github.com/ohler55/ojg v1.28.9 // indirect
	github.com/openai/openai-go v1.8.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/xavidop/mamori v1.13.0 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.4 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/api v0.307.0 // indirect
	google.golang.org/genai v1.57.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/xavidop/tamesu => ..

replace github.com/xavidop/tamesu/audio => ../audio

replace github.com/xavidop/tamesu/llm/plugins/deepgram => ./plugins/deepgram

replace github.com/xavidop/tamesu/llm/plugins/elevenlabs => ./plugins/elevenlabs

replace github.com/xavidop/tamesu/llm/plugins/cartesia => ./plugins/cartesia

replace github.com/xavidop/tamesu/llm/plugins/soniox => ./plugins/soniox

replace github.com/xavidop/tamesu/llm/plugins/assemblyai => ./plugins/assemblyai

replace github.com/xavidop/tamesu/llm/plugins/azure => ./plugins/azure

replace github.com/xavidop/tamesu/llm/plugins/openaispeech => ./plugins/openaispeech

replace github.com/xavidop/tamesu/llm/plugins/rime => ./plugins/rime

replace github.com/xavidop/tamesu/llm/plugins/googlespeech => ./plugins/googlespeech

replace github.com/xavidop/tamesu/llm/plugins/speechmatics => ./plugins/speechmatics

replace github.com/xavidop/tamesu/llm/plugins/gladia => ./plugins/gladia
