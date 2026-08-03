package hy2

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/apernet/hysteria/extras/v2/outbounds/acl/v2geo"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestDownloadAndLoadParsesOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("valid geodata"))
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "geo.dat")
	loader := &GeoLoader{Logger: zap.NewNop()}
	loadCalls := 0
	loaded, err := downloadAndLoad(loader, target, server.URL, func(path string) (string, error) {
		loadCalls++
		data, err := os.ReadFile(path)
		return string(data), err
	})
	if err != nil {
		t.Fatalf("download and load: %v", err)
	}
	if loaded != "valid geodata" {
		t.Fatalf("loaded data = %q", loaded)
	}
	if loadCalls != 1 {
		t.Fatalf("loader called %d times, want 1", loadCalls)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("download was not installed: %v", err)
	}
}

func TestDownloadAndLoadRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusBadGateway)
	}))
	defer server.Close()

	loader := &GeoLoader{Logger: zap.NewNop()}
	_, err := downloadAndLoad(loader, filepath.Join(t.TempDir(), "geo.dat"), server.URL,
		func(string) (string, error) { return "", nil })
	if err == nil {
		t.Fatal("expected an HTTP error")
	}
}

func TestGeoIPCacheIsSharedAcrossLoaderInstances(t *testing.T) {
	encoded, err := proto.Marshal(&v2geo.GeoIPList{Entry: []*v2geo.GeoIP{{CountryCode: "TEST"}}})
	if err != nil {
		t.Fatalf("marshal test GeoIP: %v", err)
	}
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatalf("write test GeoIP: %v", err)
	}

	sharedGeoData.Lock()
	sharedGeoData.geoIP = make(map[string]geoIPCacheEntry)
	sharedGeoData.Unlock()

	first, err := (&GeoLoader{GeoIPFilename: path, Logger: zap.NewNop()}).LoadGeoIP()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := (&GeoLoader{GeoIPFilename: path, Logger: zap.NewNop()}).LoadGeoIP()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	first["shared-sentinel"] = &v2geo.GeoIP{CountryCode: "SHARED"}
	if second["shared-sentinel"] != first["shared-sentinel"] {
		t.Fatal("loader instances retained duplicate maps for the same file version")
	}
}
