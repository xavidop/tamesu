module github.com/xavidop/tamesu/targets/rest

go 1.27

require (
	github.com/ohler55/ojg v1.28.9
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/xavidop/tamesu => ../..
