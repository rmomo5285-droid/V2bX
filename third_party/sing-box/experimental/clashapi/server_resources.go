//go:build go1.25

package clashapi

import (
	"archive/zip"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service/filemanager"
)

func (s *Server) checkAndDownloadExternalUI() {
	if s.externalUI == "" {
		return
	}
	entries, err := os.ReadDir(s.externalUI)
	if err != nil {
		os.MkdirAll(s.externalUI, 0o755)
	}
	if len(entries) == 0 {
		err = s.downloadExternalUI()
		if err != nil {
			s.logger.Error("download external ui error: ", err)
		}
	}
}

func (s *Server) downloadExternalUI() error {
	var downloadURL string
	if s.externalUIDownloadURL != "" {
		downloadURL = s.externalUIDownloadURL
	} else {
		downloadURL = "https://github.com/MetaCubeX/Yacd-meta/archive/gh-pages.zip"
	}
	var detour adapter.Outbound
	if s.externalUIDownloadDetour != "" {
		outbound, loaded := s.outbound.Outbound(s.externalUIDownloadDetour)
		if !loaded {
			return E.New("detour outbound not found: ", s.externalUIDownloadDetour)
		}
		detour = outbound
	} else {
		outbound := s.outbound.Default()
		detour = outbound
	}
	s.logger.Info("downloading external ui using outbound/", detour.Type(), "[", detour.Tag(), "]")
	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: C.TCPTimeout,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return detour.DialContext(ctx, network, M.ParseSocksaddr(addr))
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(s.ctx),
				RootCAs: adapter.RootPoolFromContext(s.ctx),
			},
		},
	}
	defer httpClient.CloseIdleConnections()
	response, err := httpClient.Get(downloadURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return E.New("download external ui failed: ", response.Status)
	}
	err = s.downloadZIP(response.Body, s.externalUI)
	if err != nil {
		removeAllInDirectory(s.externalUI)
	}
	return err
}

func (s *Server) downloadZIP(body io.Reader, output string) error {
	tempFile, err := filemanager.CreateTemp(s.ctx, "external-ui.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())
	const maxArchiveBytes = 128 << 20
	written, err := io.Copy(tempFile, io.LimitReader(body, maxArchiveBytes+1))
	tempFile.Close()
	if err != nil {
		return err
	}
	if written > maxArchiveBytes {
		return E.New("external UI archive exceeds 128 MiB")
	}
	reader, err := zip.OpenReader(tempFile.Name())
	if err != nil {
		return err
	}
	defer reader.Close()
	root, err := os.OpenRoot(output)
	if err != nil {
		return err
	}
	defer root.Close()
	trimDir := zipIsInSingleDirectory(reader.File)
	var totalSize uint64
	for _, file := range reader.File {
		if !filepath.IsLocal(file.Name) || strings.Contains(file.Name, "\\") || strings.Contains(file.Name, ":") {
			return E.New("unsafe external UI archive entry")
		}
		if !file.Mode().IsRegular() && !file.Mode().IsDir() {
			return E.New("non-regular external UI archive entry")
		}
		if file.FileInfo().IsDir() {
			continue
		}
		pathElements := strings.Split(file.Name, "/")
		if trimDir {
			pathElements = pathElements[1:]
		}
		savePath := filepath.Join(pathElements...)
		if !filepath.IsLocal(savePath) {
			return E.New("unsafe external UI archive path")
		}
		const maxExtractedBytes = 512 << 20
		if file.UncompressedSize64 > maxExtractedBytes-totalSize {
			return E.New("external UI extracted content exceeds 512 MiB")
		}
		totalSize += file.UncompressedSize64
		err = root.MkdirAll(filepath.Dir(savePath), 0o755)
		if err != nil {
			return err
		}
		err = downloadZIPEntry(root, file, savePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func downloadZIPEntry(root *os.Root, zipFile *zip.File, savePath string) error {
	saveFile, err := root.OpenFile(savePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer saveFile.Close()
	reader, err := zipFile.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(saveFile, io.LimitReader(reader, int64(zipFile.UncompressedSize64)+1))
	return err
}

func removeAllInDirectory(directory string) {
	dirEntries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, dirEntry := range dirEntries {
		os.RemoveAll(filepath.Join(directory, dirEntry.Name()))
	}
}

func zipIsInSingleDirectory(files []*zip.File) bool {
	var singleDirectory string
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}
		pathElements := strings.Split(file.Name, "/")
		if len(pathElements) < 2 {
			return false
		}
		if singleDirectory == "" {
			singleDirectory = pathElements[0]
		} else if singleDirectory != pathElements[0] {
			return false
		}
	}
	return true
}
