package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ExportRequest is the request body for /api/export.
type ExportRequest struct {
	Password string `json:"password"`
}

// handleExport creates a zip archive of ~/.hermes data and returns it as a file download.
// Only allowed after password verification.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	// Parse request
	var req ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Verify password
	expected := s.Config.Server.ExportPassword
	if expected != "" && req.Password != expected {
		http.Error(w, `{"error":"invalid password"}`, http.StatusUnauthorized)
		return
	}

	hermesHome := s.HermesHome
	log.Printf("[export] Generating export archive from %s", hermesHome)

	// Build the zip in memory
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// 1. Collect all .db files from ~/.hermes/
	if err := addDBFiles(zw, hermesHome); err != nil {
		log.Printf("[export] error adding db files: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"failed to add db files: %s"}`, err.Error()), http.StatusInternalServerError)
		zw.Close()
		return
	}

	// 2. Add profiles/ directory
	profilesDir := filepath.Join(hermesHome, "profiles")
	if info, err := os.Stat(profilesDir); err == nil && info.IsDir() {
		if err := addDirToZip(zw, profilesDir, "profiles"); err != nil {
			log.Printf("[export] error adding profiles: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"failed to add profiles: %s"}`, err.Error()), http.StatusInternalServerError)
			zw.Close()
			return
		}
	}

	// 3. Add shared/ directory (if exists)
	sharedDir := filepath.Join(hermesHome, "shared")
	if info, err := os.Stat(sharedDir); err == nil && info.IsDir() {
		if err := addDirToZip(zw, sharedDir, "shared"); err != nil {
			log.Printf("[export] error adding shared: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"failed to add shared: %s"}`, err.Error()), http.StatusInternalServerError)
			zw.Close()
			return
		}
	}

	// 4. Add config.yaml (root ~/.hermes/config.yaml)
	configPath := filepath.Join(hermesHome, "config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		if err := addFileToZip(zw, configPath, "config.yaml"); err != nil {
			log.Printf("[export] error adding config.yaml: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"failed to add config.yaml: %s"}`, err.Error()), http.StatusInternalServerError)
			zw.Close()
			return
		}
	}

	// 5. Add SHARED.md (if exists)
	sharedMDPath := filepath.Join(hermesHome, "SHARED.md")
	if _, err := os.Stat(sharedMDPath); err == nil {
		if err := addFileToZip(zw, sharedMDPath, "SHARED.md"); err != nil {
			log.Printf("[export] error adding SHARED.md: %v", err)
		}
	}

	// Close the zip writer to finalize
	if err := zw.Close(); err != nil {
		log.Printf("[export] error finalizing zip: %v", err)
		http.Error(w, `{"error":"failed to finalize zip"}`, http.StatusInternalServerError)
		return
	}

	zipData := buf.Bytes()
	log.Printf("[export] Export complete: %d bytes", len(zipData))

	// Set response headers for file download
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="hermes-export-%s.zip"`, filepath.Base(hermesHome)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(zipData)))
	w.WriteHeader(http.StatusOK)
	w.Write(zipData)
}

// addDBFiles adds all .db files from the Hermes home directory to the zip.
func addDBFiles(zw *zip.Writer, hermesHome string) error {
	entries, err := os.ReadDir(hermesHome)
	if err != nil {
		return fmt.Errorf("read hermes home: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		// Skip WAL and SHM files — they're SQLite temporary files
		if strings.HasSuffix(entry.Name(), "-wal") || strings.HasSuffix(entry.Name(), "-shm") {
			continue
		}
		fullPath := filepath.Join(hermesHome, entry.Name())
		if err := addFileToZip(zw, fullPath, entry.Name()); err != nil {
			return fmt.Errorf("add db file %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// addFileToZip adds a single file to the zip at the given path within the archive.
func addFileToZip(zw *zip.Writer, srcPath, arcName string) error {
	fi, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	header.Name = arcName
	header.Method = zip.Deflate

	fw, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(fw, f)
	return err
}

// addDirToZip recursively adds a directory to the zip.
// arcPrefix is the path prefix within the archive (e.g., "profiles").
func addDirToZip(zw *zip.Writer, srcDir, arcPrefix string) error {
	return filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get the relative path
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		arcName := filepath.Join(arcPrefix, relPath)

		if fi.IsDir() {
			// Add directory entry (needed for empty dirs)
			_, err := zw.Create(arcName + "/")
			return err
		}

		// Skip hidden files
		if strings.HasPrefix(fi.Name(), ".") {
			return nil
		}

		return addFileToZip(zw, path, arcName)
	})
}
