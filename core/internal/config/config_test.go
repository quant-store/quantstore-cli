package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestStoreUpdateAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store := NewStore(path)
	err := store.Update(context.Background(), func(file *File) error {
		profile := file.Profiles[DefaultProfile]
		profile.BaseURL = "http://127.0.0.1:8080"
		profile.Output = "json"
		file.Profiles[DefaultProfile] = profile
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Profiles[DefaultProfile].BaseURL; got != "http://127.0.0.1:8080" {
		t.Fatalf("base URL = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "qs_live_") {
		t.Fatal("config unexpectedly contains API key material")
	}
}

func TestStoreFailsClosedWhenPrimaryIsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	if err := store.Update(context.Background(), func(file *File) error {
		profile := file.Profiles[DefaultProfile]
		profile.Output = "json"
		file.Profiles[DefaultProfile] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(file *File) error {
		profile := file.Profiles[DefaultProfile]
		profile.Output = "yaml"
		file.Profiles[DefaultProfile] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("expected corrupt primary to fail closed, got %v", err)
	}
	backup, err := readAndDecode(path + ".bak")
	if err != nil {
		t.Fatalf("backup should remain explicitly recoverable: %v", err)
	}
	if got := backup.Profiles[DefaultProfile].Output; got != "json" {
		t.Fatalf("backup output = %q, want json", got)
	}
}

func TestStoreFailsClosedWhenPrimaryIsMissingButBackupExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	if err := store.Update(context.Background(), func(file *File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(file *File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "primary is missing") {
		t.Fatalf("expected missing primary to fail closed, got %v", err)
	}
}

func TestStoreConcurrentUpdatesDoNotLoseProfiles(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	const writers = 20
	var wg sync.WaitGroup
	errorsCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			name := fmt.Sprintf("worker-%02d", index)
			errorsCh <- store.Update(context.Background(), func(file *File) error {
				file.Profiles[name] = DefaultProfileConfig(name)
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(loaded.Profiles); got != writers+1 {
		t.Fatalf("profile count = %d, want %d", got, writers+1)
	}
}

func TestValidateRejectsRemotePlainHTTP(t *testing.T) {
	file := NewDefault()
	profile := file.Profiles[DefaultProfile]
	profile.BaseURL = "http://example.com"
	file.Profiles[DefaultProfile] = profile
	file.Checksum, _ = checksum(file)
	if err := Validate(file); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS validation error, got %v", err)
	}
}

func TestValidateRejectsCredentialMaterialInBaseURL(t *testing.T) {
	key := "qs_live_" + strings.Repeat("B", 43)
	file := NewDefault()
	profile := file.Profiles[DefaultProfile]
	profile.BaseURL = "https://" + key + ".example.com"
	file.Profiles[DefaultProfile] = profile
	file.Checksum, _ = checksum(file)
	if err := Validate(file); err == nil || !strings.Contains(err.Error(), "credential material") {
		t.Fatalf("expected credential rejection, got %v", err)
	}
}

func TestStoreRejectsSymlinkedConfigParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires host-specific privilege; covered by native runner")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(target, linkedParent); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(linkedParent, "nested", "config.json"))
	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink parent rejection, got %v", err)
	}
}
