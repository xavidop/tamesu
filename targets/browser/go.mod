module github.com/xavidop/tamesu/targets/browser

go 1.27

replace github.com/xavidop/tamesu => ../..

require (
	github.com/bmatcuk/doublestar/v4 v4.10.4
	github.com/chromedp/cdproto v0.0.0-20260714215040-dc233986426f
	github.com/chromedp/chromedp v0.16.0
	github.com/stretchr/testify v1.12.1
	github.com/xavidop/tamesu v0.0.0-00010101000000-000000000000
)

require (
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.49.0 // indirect
)
