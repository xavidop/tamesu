module github.com/xavidop/tamesu/targets/browser

go 1.27

replace github.com/xavidop/tamesu => ../..

require (
	github.com/bmatcuk/doublestar/v4 v4.10.4
	github.com/chromedp/cdproto v0.158.6
	github.com/chromedp/chromedp v0.21.0
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
)
