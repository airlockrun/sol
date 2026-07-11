package provider

import _ "embed"

//go:generate go run ./internal/updatecatalog -output catalogdata/catalog.json

//go:embed catalogdata/catalog.json
var embeddedCatalogJSON []byte
