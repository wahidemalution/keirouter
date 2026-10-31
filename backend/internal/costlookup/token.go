package costlookup

import (
	"encoding/json"
	"fmt"

	"github.com/mydisha/keirouter/backend/internal/crypto"
)

// SealToken seals a system access token for storage in a text column.
func SealToken(s *crypto.Sealer, token string) (string, error) {
	sealed, err := s.SealString(token)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// OpenToken reverses SealToken.
func OpenToken(s *crypto.Sealer, stored string) (string, error) {
	var sealed crypto.Sealed
	if err := json.Unmarshal([]byte(stored), &sealed); err != nil {
		return "", fmt.Errorf("costlookup: stored token: %w", err)
	}
	return s.OpenString(sealed)
}
