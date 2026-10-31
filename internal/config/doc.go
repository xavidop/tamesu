// Package config loads tamesu.yaml (the project configuration) and suite files,
// validates them with file and line numbers, interpolates ${VAR} and renders
// {{...}} templates, and generates the JSON Schema published under schema/.
package config
