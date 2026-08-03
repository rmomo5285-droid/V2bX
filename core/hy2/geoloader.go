package hy2

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/apernet/hysteria/extras/v2/outbounds/acl"
	"github.com/apernet/hysteria/extras/v2/outbounds/acl/v2geo"
	"go.uber.org/zap"
)

const (
	geoipFilename      = "geoip.dat"
	geoipURL           = "https://cdn.jsdelivr.net/gh/Loyalsoldier/v2ray-rules-dat@release/geoip.dat"
	geositeFilename    = "geosite.dat"
	geositeURL         = "https://cdn.jsdelivr.net/gh/Loyalsoldier/v2ray-rules-dat@release/geosite.dat"
	geoDlTmpPattern    = ".hysteria-geoloader.dlpart.*"
	geoMaxDownloadSize = 128 << 20

	geoDefaultUpdateInterval = 7 * 24 * time.Hour // 7 days
)

var _ acl.GeoLoader = (*GeoLoader)(nil)

// GeoLoader provides the on-demand GeoIP/GeoSite database
// loading functionality required by the ACL engine.
// Empty filenames = automatic download from built-in URLs.
type GeoLoader struct {
	GeoIPFilename   string
	GeoSiteFilename string
	UpdateInterval  time.Duration

	geoipMap   map[string]*v2geo.GeoIP
	geositeMap map[string]*v2geo.GeoSite

	Logger *zap.Logger
}

type geoFileStamp struct {
	size    int64
	modTime int64
}

type geoIPCacheEntry struct {
	stamp geoFileStamp
	data  map[string]*v2geo.GeoIP
}

type geoSiteCacheEntry struct {
	stamp geoFileStamp
	data  map[string]*v2geo.GeoSite
}

// Geodata parsing can temporarily consume hundreds of MiB. Keep one lock and
// cache for the whole process so multiple native Hy2 nodes cannot parse their
// own copies concurrently or retain duplicate maps for the same file version.
var sharedGeoData = struct {
	sync.Mutex
	geoIP   map[string]geoIPCacheEntry
	geoSite map[string]geoSiteCacheEntry
}{
	geoIP:   make(map[string]geoIPCacheEntry),
	geoSite: make(map[string]geoSiteCacheEntry),
}

func (l *GeoLoader) shouldDownload(filename string) (bool, error) {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		// empty files are loadable by v2geo, but we consider it broken
		return true, nil
	}
	dt := time.Since(info.ModTime())
	if l.UpdateInterval == 0 {
		return dt > geoDefaultUpdateInterval, nil
	} else {
		return dt > l.UpdateInterval, nil
	}
}

func downloadAndLoad[T any](l *GeoLoader, filename, url string, load func(filename string) (T, error)) (T, error) {
	var zero T
	l.geoDownloadFunc(filename, url)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		l.geoDownloadErrFunc(err)
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		err = fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		l.geoDownloadErrFunc(err)
		return zero, err
	}

	dir := filepath.Dir(filename)
	f, err := os.CreateTemp(dir, geoDlTmpPattern)
	if err != nil {
		l.geoDownloadErrFunc(err)
		return zero, err
	}
	defer os.Remove(f.Name())

	written, err := io.Copy(f, io.LimitReader(resp.Body, geoMaxDownloadSize+1))
	if err != nil {
		f.Close()
		l.geoDownloadErrFunc(err)
		return zero, err
	}
	if written > geoMaxDownloadSize {
		f.Close()
		err = fmt.Errorf("download exceeds %d bytes", geoMaxDownloadSize)
		l.geoDownloadErrFunc(err)
		return zero, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		l.geoDownloadErrFunc(err)
		return zero, err
	}
	if err = f.Close(); err != nil {
		l.geoDownloadErrFunc(err)
		return zero, err
	}

	loaded, err := load(f.Name())
	if err != nil {
		l.geoDownloadErrFunc(fmt.Errorf("integrity check failed: %w", err))
		return zero, err
	}
	err = os.Rename(f.Name(), filename)
	if err != nil {
		l.geoDownloadErrFunc(fmt.Errorf("rename failed: %w", err))
		return zero, err
	}
	return loaded, nil
}
func (l *GeoLoader) LoadGeoIP() (map[string]*v2geo.GeoIP, error) {
	sharedGeoData.Lock()
	defer sharedGeoData.Unlock()
	if l.geoipMap != nil {
		return l.geoipMap, nil
	}
	autoDL := false
	filename := l.GeoIPFilename
	if filename == "" {
		autoDL = true
		filename = geoipFilename
	}
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	if autoDL {
		shouldDownload, err := l.shouldDownload(filename)
		if err != nil {
			return nil, err
		}
		if !shouldDownload {
			m, err := loadSharedGeoIP(filename)
			if err == nil {
				l.geoipMap = m
				return m, nil
			}
			// file is broken, download it again
		}
		m, err := downloadAndLoad(l, filename, geoipURL, v2geo.LoadGeoIP)
		if err != nil {
			// as long as the previous download exists, fallback to it
			if _, serr := os.Stat(filename); os.IsNotExist(serr) {
				return nil, err
			}
		} else {
			cacheGeoIP(filename, m)
			l.geoipMap = m
			return m, nil
		}
	}
	m, err := loadSharedGeoIP(filename)
	if err != nil {
		return nil, err
	}
	l.geoipMap = m
	return m, nil
}

func (l *GeoLoader) LoadGeoSite() (map[string]*v2geo.GeoSite, error) {
	sharedGeoData.Lock()
	defer sharedGeoData.Unlock()
	if l.geositeMap != nil {
		return l.geositeMap, nil
	}
	autoDL := false
	filename := l.GeoSiteFilename
	if filename == "" {
		autoDL = true
		filename = geositeFilename
	}
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	if autoDL {
		shouldDownload, err := l.shouldDownload(filename)
		if err != nil {
			return nil, err
		}
		if !shouldDownload {
			m, err := loadSharedGeoSite(filename)
			if err == nil {
				l.geositeMap = m
				return m, nil
			}
			// file is broken, download it again
		}
		m, err := downloadAndLoad(l, filename, geositeURL, v2geo.LoadGeoSite)
		if err != nil {
			// as long as the previous download exists, fallback to it
			if _, serr := os.Stat(filename); os.IsNotExist(serr) {
				return nil, err
			}
		} else {
			cacheGeoSite(filename, m)
			l.geositeMap = m
			return m, nil
		}
	}
	m, err := loadSharedGeoSite(filename)
	if err != nil {
		return nil, err
	}
	l.geositeMap = m
	return m, nil
}

func fileStamp(filename string) (geoFileStamp, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return geoFileStamp{}, err
	}
	return geoFileStamp{size: info.Size(), modTime: info.ModTime().UnixNano()}, nil
}

func loadSharedGeoIP(filename string) (map[string]*v2geo.GeoIP, error) {
	stamp, err := fileStamp(filename)
	if err != nil {
		return nil, err
	}
	if cached, ok := sharedGeoData.geoIP[filename]; ok && cached.stamp == stamp {
		return cached.data, nil
	}
	m, err := v2geo.LoadGeoIP(filename)
	if err != nil {
		return nil, err
	}
	sharedGeoData.geoIP[filename] = geoIPCacheEntry{stamp: stamp, data: m}
	return m, nil
}

func cacheGeoIP(filename string, data map[string]*v2geo.GeoIP) {
	if stamp, err := fileStamp(filename); err == nil {
		sharedGeoData.geoIP[filename] = geoIPCacheEntry{stamp: stamp, data: data}
	}
}

func loadSharedGeoSite(filename string) (map[string]*v2geo.GeoSite, error) {
	stamp, err := fileStamp(filename)
	if err != nil {
		return nil, err
	}
	if cached, ok := sharedGeoData.geoSite[filename]; ok && cached.stamp == stamp {
		return cached.data, nil
	}
	m, err := v2geo.LoadGeoSite(filename)
	if err != nil {
		return nil, err
	}
	sharedGeoData.geoSite[filename] = geoSiteCacheEntry{stamp: stamp, data: m}
	return m, nil
}

func cacheGeoSite(filename string, data map[string]*v2geo.GeoSite) {
	if stamp, err := fileStamp(filename); err == nil {
		sharedGeoData.geoSite[filename] = geoSiteCacheEntry{stamp: stamp, data: data}
	}
}

func (l *GeoLoader) geoDownloadFunc(filename, url string) {
	l.Logger.Info("downloading database", zap.String("filename", filename), zap.String("url", url))
}

func (l *GeoLoader) geoDownloadErrFunc(err error) {
	if err != nil {
		l.Logger.Error("failed to download database", zap.Error(err))
	}
}
