package runtimeapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/pkg/sentinel"
)

const (
	defaultUserKeySecretNamespace = sentinel.PlatformNamespace
	defaultUserKeySecretName      = "mcp-user-api-keys" // #nosec G101 -- Kubernetes secret resource name, not credential material.
	userKeySecretPayloadKey       = "records.json"
)

type userAPIKeyRecord struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Name      string     `json:"name"`
	KeyHash   string     `json:"key_hash"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type userAPIKeyStore interface {
	AuthenticateUserAPIKey(ctx context.Context, rawKey string) (principal, bool, error)
	ListUserAPIKeys(ctx context.Context, userID string) ([]userAPIKeySummary, error)
	CreateUserAPIKey(ctx context.Context, userID, name string) (userAPIKeySummary, string, error)
	RevokeUserAPIKey(ctx context.Context, userID, id string) (userAPIKeySummary, error)
}

func (s *RuntimeServer) userKeySecretNamespace() string {
	if v := strings.TrimSpace(os.Getenv("USER_API_KEY_NAMESPACE")); v != "" {
		return v
	}
	return defaultUserKeySecretNamespace
}

func (s *RuntimeServer) userKeySecretName() string {
	if v := strings.TrimSpace(os.Getenv("USER_API_KEY_SECRET_NAME")); v != "" {
		return v
	}
	return defaultUserKeySecretName
}

// AuthenticateUserAPIKey validates a raw user API key against the Kubernetes-backed key store.
func (s *RuntimeServer) AuthenticateUserAPIKey(ctx context.Context, rawKey string) (principal, bool, error) {
	if s.k8sClients == nil {
		return principal{}, false, nil
	}
	records, err := s.loadUserAPIKeyRecords(ctx)
	if err != nil {
		return principal{}, false, err
	}
	targetHash := hashAPIKey(rawKey)
	for i := range records {
		rec := records[i]
		if rec.RevokedAt != nil {
			continue
		}
		if rec.KeyHash != targetHash {
			continue
		}
		return principal{
			Role:     roleUser,
			Subject:  rec.UserID,
			AuthType: "user_api_key",
			APIKeyID: rec.ID,
		}, true, nil
	}
	return principal{}, false, nil
}

// ListUserAPIKeys returns metadata for API keys owned by one platform user.
func (s *RuntimeServer) ListUserAPIKeys(ctx context.Context, userID string) ([]userAPIKeySummary, error) {
	if s.k8sClients == nil {
		return nil, errors.New("kubernetes not available")
	}
	records, err := s.loadUserAPIKeyRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]userAPIKeySummary, 0, len(records))
	for i := range records {
		rec := records[i]
		if rec.UserID != userID {
			continue
		}
		out = append(out, keySummary(rec))
	}
	return out, nil
}

// CreateUserAPIKey stores a new hashed user API key and returns the one-time raw key value.
func (s *RuntimeServer) CreateUserAPIKey(ctx context.Context, userID, name string) (userAPIKeySummary, string, error) {
	if s.k8sClients == nil {
		return userAPIKeySummary{}, "", errors.New("kubernetes not available")
	}
	if strings.TrimSpace(userID) == "" {
		return userAPIKeySummary{}, "", errors.New("user id required")
	}
	if strings.TrimSpace(name) == "" {
		return userAPIKeySummary{}, "", errors.New("name required")
	}
	rawKey, err := generateAPIKeyValue()
	if err != nil {
		return userAPIKeySummary{}, "", err
	}
	now := time.Now().UTC()
	keyID, err := newKeyID()
	if err != nil {
		return userAPIKeySummary{}, "", err
	}
	rec := userAPIKeyRecord{
		ID:        keyID,
		UserID:    userID,
		Name:      strings.TrimSpace(name),
		KeyHash:   hashAPIKey(rawKey),
		Prefix:    keyPrefix(rawKey),
		CreatedAt: now,
	}

	err = s.updateUserAPIKeyRecords(ctx, func(records []userAPIKeyRecord) ([]userAPIKeyRecord, error) {
		records = append(records, rec)
		return records, nil
	})
	if err != nil {
		return userAPIKeySummary{}, "", err
	}
	return keySummary(rec), rawKey, nil
}

// RevokeUserAPIKey marks a user's API key revoked and returns its final metadata.
func (s *RuntimeServer) RevokeUserAPIKey(ctx context.Context, userID, id string) (userAPIKeySummary, error) {
	if s.k8sClients == nil {
		return userAPIKeySummary{}, errors.New("kubernetes not available")
	}
	var out userAPIKeySummary
	var found bool
	err := s.updateUserAPIKeyRecords(ctx, func(records []userAPIKeyRecord) ([]userAPIKeyRecord, error) {
		now := time.Now().UTC()
		for i := range records {
			if records[i].ID != id || records[i].UserID != userID {
				continue
			}
			if records[i].RevokedAt == nil {
				records[i].RevokedAt = &now
			}
			out = keySummary(records[i])
			found = true
			break
		}
		if !found {
			return nil, apierrors.NewNotFound(corev1.Resource("userApiKey"), id)
		}
		return records, nil
	})
	if err != nil {
		return userAPIKeySummary{}, err
	}
	return out, nil
}

func (s *RuntimeServer) loadUserAPIKeyRecords(ctx context.Context) ([]userAPIKeyRecord, error) {
	secret, err := s.k8sClients.Clientset.CoreV1().Secrets(s.userKeySecretNamespace()).Get(ctx, s.userKeySecretName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []userAPIKeyRecord{}, nil
		}
		return nil, err
	}
	raw := secret.Data[userKeySecretPayloadKey]
	if len(raw) == 0 {
		return []userAPIKeyRecord{}, nil
	}
	var records []userAPIKeyRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("parse %s/%s: %w", s.userKeySecretNamespace(), s.userKeySecretName(), err)
	}
	return records, nil
}

func (s *RuntimeServer) updateUserAPIKeyRecords(ctx context.Context, mut func([]userAPIKeyRecord) ([]userAPIKeyRecord, error)) error {
	const maxAttempts = 4
	ns := s.userKeySecretNamespace()
	name := s.userKeySecretName()

	for i := 0; i < maxAttempts; i++ {
		secret, err := s.k8sClients.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		var records []userAPIKeyRecord
		if err == nil {
			raw := secret.Data[userKeySecretPayloadKey]
			if len(raw) > 0 {
				if unmarshalErr := json.Unmarshal(raw, &records); unmarshalErr != nil {
					return fmt.Errorf("parse %s/%s: %w", ns, name, unmarshalErr)
				}
			}
		}

		next, mutErr := mut(records)
		if mutErr != nil {
			return mutErr
		}
		payload, marshalErr := json.Marshal(next)
		if marshalErr != nil {
			return marshalErr
		}

		if err == nil {
			if secret.Data == nil {
				secret.Data = map[string][]byte{}
			}
			secret.Data[userKeySecretPayloadKey] = payload
			_, updateErr := s.k8sClients.Clientset.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
			if apierrors.IsConflict(updateErr) {
				continue
			}
			return updateErr
		}

		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				userKeySecretPayloadKey: payload,
			},
		}
		_, createErr := s.k8sClients.Clientset.CoreV1().Secrets(ns).Create(ctx, newSecret, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(createErr) {
			continue
		}
		return createErr
	}

	return errors.New("failed to update user api keys due to repeated conflicts")
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func keyPrefix(raw string) string {
	if len(raw) <= 8 {
		return raw
	}
	return raw[:8]
}

func keySummary(rec userAPIKeyRecord) userAPIKeySummary {
	return userAPIKeySummary{
		ID:        rec.ID,
		Name:      rec.Name,
		Prefix:    rec.Prefix,
		CreatedAt: rec.CreatedAt,
		Revoked:   rec.RevokedAt != nil,
		RevokedAt: rec.RevokedAt,
	}
}

func newKeyID() (string, error) {
	token, err := randomURLToken(12)
	if err != nil {
		return "", err
	}
	return "uk_" + token, nil
}

func generateAPIKeyValue() (string, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	return "mcpu_" + token, nil
}

func randomURLToken(rawBytes int) (string, error) {
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
