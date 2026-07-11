package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/airlockrun/sol/provider"
)

const maxCatalogSize = 16 << 20

func main() {
	output := flag.String("output", "", "output catalog path")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "-output is required")
		os.Exit(2)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(provider.CatalogURL)
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fatal(fmt.Errorf("catalog returned status %d", resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogSize+1))
	if err != nil {
		fatal(err)
	}
	if len(raw) > maxCatalogSize {
		fatal(fmt.Errorf("catalog exceeds %d bytes", maxCatalogSize))
	}
	if err := provider.ValidateCatalogJSON(raw); err != nil {
		fatal(err)
	}

	var formatted bytes.Buffer
	if err := jsonIndent(&formatted, raw); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*output, formatted.Bytes(), 0o644); err != nil {
		fatal(err)
	}
}

func jsonIndent(dst *bytes.Buffer, src []byte) error {
	if err := json.Indent(dst, src, "", "  "); err != nil {
		return err
	}
	dst.WriteByte('\n')
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
