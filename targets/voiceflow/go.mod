module github.com/xavidop/tamesu/targets/voiceflow

go 1.27

require (
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0
)

require go.yaml.in/yaml/v3 v3.0.6 // indirect

replace github.com/xavidop/tamesu => ../..
