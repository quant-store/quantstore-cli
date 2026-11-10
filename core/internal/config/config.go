package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/quant-store/quantstore-cli/core/internal/redact"
)

const (
	SchemaVersion  = 1
	DefaultProfile = "default"
	DefaultBaseURL = "https://api.quant-store.com"
)

var profileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Profile struct {
	BaseURL        string `json:"base_url"`
	Output         string `json:"output"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	CredentialRef  string `json:"credential_ref"`
}

type File struct {
	SchemaVersion  int                `json:"schema_version"`
	Generation     uint64             `json:"generation"`
	Checksum       string             `json:"checksum"`
	CurrentProfile string             `json:"current_profile"`
	Profiles       map[string]Profile `json:"profiles"`
}

type Store struct {
	path        string
	lockTimeout time.Duration
}

func DefaultPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(root, "QuantStore", "qs", "config.json"), nil
}

func NewStore(path string) *Store {
	return &Store{path: path, lockTimeout: 2 * time.Second}
}

func NewDefault() File {
	file := File{
		SchemaVersion:  SchemaVersion,
		CurrentProfile: DefaultProfile,
		Profiles: map[string]Profile{
			DefaultProfile: DefaultProfileConfig(DefaultProfile),
		},
	}
	file.Checksum, _ = checksum(file)
	return file
}

func DefaultProfileConfig(name string) Profile {
	return Profile{
		BaseURL:        DefaultBaseURL,
		Output:         "auto",
		TimeoutSeconds: 30,
		CredentialRef:  "os-keyring:profile/" + name,
	}
}

func NormalizeProfileName(name string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if !profileNamePattern.MatchString(normalized) {
		return "", errors.New("profile name must contain only lowercase letters, digits, '.', '_' or '-' and be at most 64 characters")
	}
	return normalized, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Load(ctx context.Context) (File, error) {
	var result File
	err := s.withLock(ctx, func() error {
		loaded, _, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	return result, err
}

func (s *Store) Update(ctx context.Context, mutate func(*File) error) error {
	return s.withLock(ctx, func() error {
		current, currentRaw, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if err := mutate(&current); err != nil {
			return err
		}
		current.SchemaVersion = SchemaVersion
		current.Generation++
		current.Checksum, err = checksum(current)
		if err != nil {
			return err
		}
		if err := Validate(current); err != nil {
			return err
		}
		encoded, err := marshal(current)
		if err != nil {
			return err
		}
		return s.writeUnlocked(encoded, currentRaw)
	})
}

func (s *Store) withLock(ctx context.Context, fn func() error) error {
	if strings.TrimSpace(s.path) == "" {
		return errors.New("config path is empty")
	}
	if err := rejectExistingParentSymlinks(filepath.Dir(s.path), 3); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := rejectSymlink(filepath.Dir(s.path)); err != nil {
		return err
	}
	if err := rejectSymlink(filepath.Dir(filepath.Dir(s.path))); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("secure config directory: %w", err)
	}
	if err := verifyPrivateDirectory(filepath.Dir(s.path)); err != nil {
		return err
	}
	lockPath := s.path + ".lock"
	if err := rejectSymlink(lockPath); err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()
	locked, err := fileLock.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	if !locked {
		return errors.New("config is busy; retry the command")
	}
	defer func() { _ = fileLock.Unlock() }()
	return fn()
}

func (s *Store) loadUnlocked() (File, []byte, error) {
	primary, primaryErr := readAndDecode(s.path)
	if primaryErr == nil {
		raw, err := os.ReadFile(s.path)
		return primary, raw, err
	}
	if errors.Is(primaryErr, os.ErrNotExist) {
		if _, backupErr := os.Lstat(s.path + ".bak"); errors.Is(backupErr, os.ErrNotExist) {
			return NewDefault(), nil, nil
		} else if backupErr != nil {
			return File{}, nil, fmt.Errorf("inspect config backup: %w", backupErr)
		}
		return File{}, nil, errors.New("config primary is missing while a backup exists; restore or remove it explicitly")
	}
	return File{}, nil, fmt.Errorf("read config: %w", primaryErr)
}

func readAndDecode(path string) (File, error) {
	var file File
	if err := rejectSymlink(path); err != nil {
		return file, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return file, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return file, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return file, fmt.Errorf("decode %s: trailing JSON value", filepath.Base(path))
	}
	if err := Validate(file); err != nil {
		return file, fmt.Errorf("validate %s: %w", filepath.Base(path), err)
	}
	return file, nil
}

func Validate(file File) error {
	if file.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported config schema_version %d", file.SchemaVersion)
	}
	if len(file.Profiles) == 0 {
		return errors.New("config must contain at least one profile")
	}
	if _, ok := file.Profiles[file.CurrentProfile]; !ok {
		return errors.New("current_profile does not exist")
	}
	names := make([]string, 0, len(file.Profiles))
	for name := range file.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if normalized, err := NormalizeProfileName(name); err != nil || normalized != name {
			return fmt.Errorf("invalid profile name %q", name)
		}
		profile := file.Profiles[name]
		if err := validateProfile(name, profile); err != nil {
			return err
		}
	}
	expected, err := checksum(file)
	if err != nil {
		return err
	}
	if file.Checksum != expected {
		return errors.New("config checksum mismatch")
	}
	return nil
}

func validateProfile(name string, profile Profile) error {
	if redact.ContainsCredential(profile.BaseURL) {
		return fmt.Errorf("profile %q base_url contains credential material", name)
	}
	parsed, err := url.Parse(profile.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("profile %q has invalid base_url", name)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("profile %q base_url must not contain a path", name)
	}
	hostname := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" {
		ip := net.ParseIP(hostname)
		if parsed.Scheme != "http" || (hostname != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return fmt.Errorf("profile %q base_url must use HTTPS; HTTP is allowed only for localhost", name)
		}
	}
	switch profile.Output {
	case "auto", "table", "json", "jsonl", "yaml", "raw":
	default:
		return fmt.Errorf("profile %q has invalid output mode", name)
	}
	if profile.TimeoutSeconds < 1 || profile.TimeoutSeconds > 300 {
		return fmt.Errorf("profile %q timeout_seconds must be between 1 and 300", name)
	}
	if profile.CredentialRef != "os-keyring:profile/"+name {
		return fmt.Errorf("profile %q has invalid credential_ref", name)
	}
	return nil
}

func checksum(file File) (string, error) {
	payload := struct {
		SchemaVersion  int                `json:"schema_version"`
		Generation     uint64             `json:"generation"`
		CurrentProfile string             `json:"current_profile"`
		Profiles       map[string]Profile `json:"profiles"`
	}{file.SchemaVersion, file.Generation, file.CurrentProfile, file.Profiles}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode config checksum payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func marshal(file File) ([]byte, error) {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(raw, '\n'), nil
}

func (s *Store) writeUnlocked(next, previous []byte) error {
	if len(previous) > 0 {
		if err := writeDurable(s.path+".bak", previous); err != nil {
			return fmt.Errorf("write config backup: %w", err)
		}
	}
	if err := writeDurable(s.path, next); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func writeDurable(path string, data []byte) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".qs-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink config path %s", path)
	}
	return nil
}

func rejectExistingParentSymlinks(path string, depth int) error {
	current := filepath.Clean(path)
	for index := 0; index < depth; index++ {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink config parent %s", current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return nil
}
