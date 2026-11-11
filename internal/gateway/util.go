package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

type stringsBuilder struct{ b strings.Builder }

func (s *stringsBuilder) Write(p []byte) { s.b.Write(p) }
func (s *stringsBuilder) String() string { return s.b.String() }

func probeURL(ctx context.Context, url string) error {
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return err
	}
	return nil
}
