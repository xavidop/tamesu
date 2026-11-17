module github.com/xavidop/tamesu/targets/desktop

go 1.27

replace github.com/xavidop/tamesu => ../..

require (
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0-00010101000000-000000000000
)

require go.yaml.in/yaml/v3 v3.0.6 // indirect
