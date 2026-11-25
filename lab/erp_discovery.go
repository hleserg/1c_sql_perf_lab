package lab

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

//go:embed discover_document.ps1
var discoverDocumentPS string
var metadataName = regexp.MustCompile(`^[\pL_][\pL\pN_]{0,127}$`)

// Pin one existing reference before the first sample so every session reads the same document.
func FindWorkloadDocument(ctx context.Context, p Profile) (string, error) {
	if p.ERPServer == "" || p.ERPDatabase == "" || !metadataName.MatchString(p.Document) {
		return "", fmt.Errorf("не заданы стендовая база 1С или имя документа")
	}
	ctx, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(discoverDocumentPS))
	cmd.Env = append(os.Environ(), "PERFLAB_ERP_SERVER="+p.ERPServer, "PERFLAB_ERP_DATABASE="+p.ERPDatabase, "PERFLAB_DOCUMENT="+p.Document)
	var stderr racOutput
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("не удалось выбрать документ через COM 1С в базе %s: %w · %s", p.ERPDatabase, err, platformFailure(stderr.data.Bytes()))
	}
	v := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	if !uuid.MatchString(v) {
		return "", fmt.Errorf("не найден документ %s для повторяемой нагрузки", p.Document)
	}
	return v, nil
}
