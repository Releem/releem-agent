package repeater

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestConfigurationRequestSendsApplyModeAndDoesNotWriteJSON(t *testing.T) {
	tempDir := t.TempDir()
	configuration := &config.Config{ApiKey: "key", ReleemConfDir: tempDir}
	logger := *logging.Init("repeater-test", true, false, io.Discard)
	repeater := NewReleemConfigurationsRepeater(configuration, logger)

	originalClient := newHTTPClient
	t.Cleanup(func() { newHTTPClient = originalClient })
	newHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if got := request.Header.Get("X-Releem-Apply-Mode"); got != "dynamic" {
				t.Errorf("configuration request apply mode = %q, want %q", got, "dynamic")
			}
			if got := request.Header.Get("Accept"); got != "application/json" {
				t.Errorf("configuration request Accept = %q, want application/json", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"max_connections":"200"}`)),
				Header:     make(http.Header),
			}, nil
		})}
	}

	result, err := repeater.ProcessMetrics(
		configuration,
		models.Metrics{},
		models.ModeType{Name: "Configurations", Type: "GetJson", ApplyMode: "dynamic"},
	)
	if err != nil {
		t.Fatalf("ProcessMetrics(GetJson dynamic) error = %v, want nil", err)
	}
	if result != `{"max_connections":"200"}` {
		t.Errorf("ProcessMetrics(GetJson dynamic) = %q, want JSON response", result)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", tempDir, err)
	}
	if len(entries) != 0 {
		t.Errorf("ProcessMetrics(GetJson dynamic) wrote %d files, want 0", len(entries))
	}
}
