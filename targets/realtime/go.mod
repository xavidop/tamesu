module github.com/xavidop/tamesu/targets/realtime

go 1.27

require (
	github.com/coder/websocket v1.8.15
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0
	github.com/xavidop/tamesu/audio v0.0.0
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/xavidop/tamesu => ../..

replace github.com/xavidop/tamesu/audio => ../../audio
