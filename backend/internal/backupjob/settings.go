package backupjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/httputil"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Settings keys. Each value is stored in the settings table, so a full backup
// carries the schedule and its sealed secrets to a new server.
const (
	SettingsKey = "backup.auto"
	StateKey    = "backup.auto.state"
	HistoryKey  = "backup.auto.history"
	LeaseKey    = "backup.auto.lease"
)

// Destinations and FTP TLS modes.
const (
	DestS3      = "s3"
	DestFTP     = "ftp"
	TLSExplicit = "explicit"
	TLSNone     = "none"
)

// Bounds, from the spec.
const (
	MaxIntervalHours   = 720
	MaxKeep            = 365
	MaxUploadLimitKBps = 1_000_000
)

// Settings configures scheduled offsite backups. Secret fields are never
// serialized as-is: Save seals them and Public hides them.
type Settings struct {
	Enabled         bool        `json:"enabled"`
	Destination     string      `json:"destination"`
	IntervalHours   int         `json:"interval_hours"`
	Keep            int         `json:"keep"`
	UploadLimitKBps int         `json:"upload_limit_kbps"`
	Passphrase      string      `json:"-"`
	S3              S3Settings  `json:"s3"`
	FTP             FTPSettings `json:"ftp"`
}

// S3Settings is an S3 or S3-compatible bucket. An empty Endpoint means AWS.
type S3Settings struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"-"`
	PathStyle       bool   `json:"path_style"`
}

// FTPSettings is an FTP server, with explicit TLS (FTPS) or none.
type FTPSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"-"`
	Folder   string `json:"folder"`
}

// Defaults are the settings before anything is saved.
func Defaults() Settings {
	return Settings{
		Destination:   DestS3,
		IntervalHours: 24,
		Keep:          7,
		FTP:           FTPSettings{Port: 21, TLS: TLSExplicit},
	}
}

// FieldError names the setting that failed validation.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Validate checks bounds always, and completeness when Enabled.
func (s Settings) Validate() error {
	if s.Destination != DestS3 && s.Destination != DestFTP {
		return fieldErr("destination", "must be s3 or ftp")
	}
	if s.IntervalHours < 1 || s.IntervalHours > MaxIntervalHours {
		return fieldErr("interval_hours", "must be between 1 and %d", MaxIntervalHours)
	}
	if s.Keep < 1 || s.Keep > MaxKeep {
		return fieldErr("keep", "must be between 1 and %d", MaxKeep)
	}
	if s.UploadLimitKBps < 0 || s.UploadLimitKBps > MaxUploadLimitKBps {
		return fieldErr("upload_limit_kbps", "must be between 0 and %d", MaxUploadLimitKBps)
	}
	if s.Passphrase != "" && len(strings.TrimSpace(s.Passphrase)) < MinPassphrase {
		return fieldErr("passphrase", "must be at least %d characters", MinPassphrase)
	}
	if s.FTP.TLS != TLSExplicit && s.FTP.TLS != TLSNone {
		return fieldErr("ftp.tls", "must be explicit or none")
	}
	if s.FTP.Port < 0 || s.FTP.Port > 65535 {
		return fieldErr("ftp.port", "must be a TCP port")
	}
	if err := validateEndpoint(s.S3.Endpoint); err != nil {
		return err
	}
	if s.FTP.Host != "" {
		if err := httputil.ValidateHost(s.FTP.Host); err != nil {
			return fieldErr("ftp.host", "%v", err)
		}
	}
	if !s.Enabled {
		return nil
	}
	if s.Passphrase == "" {
		return fieldErr("passphrase", "must be at least %d characters", MinPassphrase)
	}
	switch s.Destination {
	case DestS3:
		for _, f := range []struct{ name, v string }{
			{"s3.region", s.S3.Region}, {"s3.bucket", s.S3.Bucket},
			{"s3.access_key_id", s.S3.AccessKeyID}, {"s3.secret_access_key", s.S3.SecretAccessKey},
		} {
			if strings.TrimSpace(f.v) == "" {
				return fieldErr(f.name, "is required")
			}
		}
	case DestFTP:
		for _, f := range []struct{ name, v string }{
			{"ftp.host", s.FTP.Host}, {"ftp.username", s.FTP.Username}, {"ftp.password", s.FTP.Password},
		} {
			if strings.TrimSpace(f.v) == "" {
				return fieldErr(f.name, "is required")
			}
		}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fieldErr("s3.endpoint", "must be a URL such as https://s3.example.com")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && httputil.PrivateBaseURLAllowed()) {
		return fieldErr("s3.endpoint", "must use https (http needs security.allow_private_base_url)")
	}
	if err := httputil.ValidateOutboundURL(endpoint); err != nil {
		return fieldErr("s3.endpoint", "%v", err)
	}
	return nil
}

// Merge applies incoming over stored, keeping each stored secret whose
// incoming value is empty.
func Merge(stored, incoming Settings) Settings {
	out := incoming
	if out.Passphrase == "" {
		out.Passphrase = stored.Passphrase
	}
	if out.S3.SecretAccessKey == "" {
		out.S3.SecretAccessKey = stored.S3.SecretAccessKey
	}
	if out.FTP.Password == "" {
		out.FTP.Password = stored.FTP.Password
	}
	return out
}

// SecretState tells the UI whether a secret is stored, never its value.
type SecretState struct {
	Set bool `json:"set"`
}

// PublicSettings is Settings as the dashboard sees it.
type PublicSettings struct {
	Settings
	Passphrase SecretState `json:"passphrase"`
	S3         PublicS3    `json:"s3"`
	FTP        PublicFTP   `json:"ftp"`
}

// PublicS3 is S3Settings without the secret key.
type PublicS3 struct {
	S3Settings
	SecretAccessKey SecretState `json:"secret_access_key"`
}

// PublicFTP is FTPSettings without the password.
type PublicFTP struct {
	FTPSettings
	Password SecretState `json:"password"`
}

// Public returns the settings with every secret replaced by its state.
func (s Settings) Public() PublicSettings {
	p := PublicSettings{
		Settings:   s,
		Passphrase: SecretState{Set: s.Passphrase != ""},
		S3:         PublicS3{S3Settings: s.S3, SecretAccessKey: SecretState{Set: s.S3.SecretAccessKey != ""}},
		FTP:        PublicFTP{FTPSettings: s.FTP, Password: SecretState{Set: s.FTP.Password != ""}},
	}
	p.Settings.Passphrase, p.Settings.S3.SecretAccessKey, p.Settings.FTP.Password = "", "", ""
	p.S3.S3Settings.SecretAccessKey, p.FTP.FTPSettings.Password = "", ""
	return p
}

// NormalizePrefix turns a folder into "a/b/" form, or "" for the root.
func NormalizePrefix(p string) string {
	var parts []string
	for _, seg := range strings.Split(strings.TrimSpace(p), "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "/") + "/"
}

// stored is the persisted form: plain fields plus each secret sealed under
// the master key with crypto.Sealed's wrapped_dek/ciphertext JSON tags, so a
// full backup re-keys them.
type stored struct {
	Settings
	PassphraseSealed  *crypto.Sealed `json:"passphrase_sealed,omitempty"`
	S3SecretSealed    *crypto.Sealed `json:"s3_secret_sealed,omitempty"`
	FTPPasswordSealed *crypto.Sealed `json:"ftp_password_sealed,omitempty"`
}

// Load reads the settings; a missing row yields Defaults.
func Load(ctx context.Context, get func(context.Context, string) (string, error), box Opener) (Settings, error) {
	raw, err := get(ctx, SettingsKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	st := stored{Settings: Defaults()}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return Settings{}, fmt.Errorf("backupjob: decode settings: %w", err)
	}
	s := st.Settings
	for _, f := range []struct {
		sealed *crypto.Sealed
		dst    *string
		name   string
	}{
		{st.PassphraseSealed, &s.Passphrase, "passphrase"},
		{st.S3SecretSealed, &s.S3.SecretAccessKey, "s3 secret key"},
		{st.FTPPasswordSealed, &s.FTP.Password, "ftp password"},
	} {
		if f.sealed == nil {
			continue
		}
		plain, err := box.OpenString(*f.sealed)
		if err != nil {
			return Settings{}, fmt.Errorf("backupjob: open %s: %w", f.name, err)
		}
		*f.dst = plain
	}
	return s, nil
}

// Save seals the secrets and writes the settings.
func Save(ctx context.Context, set func(context.Context, string, string) error, box Opener, s Settings) error {
	st := stored{Settings: s}
	st.Settings.Passphrase, st.Settings.S3.SecretAccessKey, st.Settings.FTP.Password = "", "", ""
	for _, f := range []struct {
		plain string
		dst   **crypto.Sealed
	}{
		{s.Passphrase, &st.PassphraseSealed},
		{s.S3.SecretAccessKey, &st.S3SecretSealed},
		{s.FTP.Password, &st.FTPPasswordSealed},
	} {
		if f.plain == "" {
			continue
		}
		sealed, err := box.SealString(f.plain)
		if err != nil {
			return fmt.Errorf("backupjob: seal secret: %w", err)
		}
		*f.dst = &sealed
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return set(ctx, SettingsKey, string(raw))
}
